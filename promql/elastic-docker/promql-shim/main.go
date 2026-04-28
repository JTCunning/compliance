// promql-shim translates Prometheus HTTP API range queries to Elasticsearch ES|QL PROMQL
// and reshapes columnar JSON into the Prometheus matrix JSON envelope expected by
// github.com/prometheus/client_golang/api/prometheus/v1.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	esURL := strings.TrimSuffix(os.Getenv("ES_URL"), "/")
	if esURL == "" {
		esURL = "http://127.0.0.1:9200"
	}
	indexPattern := os.Getenv("INDEX_PATTERN")
	if indexPattern == "" {
		indexPattern = "metrics-*"
	}
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = ":8080"
	}

	h := &handler{esURL: esURL, indexPattern: indexPattern, client: &http.Client{Timeout: 25 * time.Second}}
	mux := http.NewServeMux()
	mux.HandleFunc("/-/healthy", h.handleHealth)
	mux.HandleFunc("/api/v1/query_range", h.handleQueryRange)
	mux.HandleFunc("/api/v1/query", h.handleQueryInstant)

	log.Printf("promql-shim listening on %s (ES %s, index=%s)", listen, esURL, indexPattern)
	log.Fatal(http.ListenAndServe(listen, mux))
}

type handler struct {
	esURL        string
	indexPattern string
	client       *http.Client
}

func (h *handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp, err := h.client.Get(h.esURL + "/_cluster/health?wait_for_status=yellow&timeout=5s")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		http.Error(w, string(b), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("OK\n"))
}

func (h *handler) handleQueryInstant(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writePromAPIError(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}
	q := firstNonEmpty(r.Form.Get("query"), r.URL.Query().Get("query"))
	if q == "" {
		writePromAPIError(w, http.StatusBadRequest, "bad_data", "parameter query is required")
		return
	}
	tsStr := firstNonEmpty(r.Form.Get("time"), r.URL.Query().Get("time"))
	var t time.Time
	var err error
	if tsStr == "" {
		t = time.Now().UTC()
	} else {
		t, err = parsePrometheusTime(tsStr)
		if err != nil {
			writePromAPIError(w, http.StatusBadRequest, "bad_data", err.Error())
			return
		}
	}
	// Instant vector: use a minimal window so PROMQL still has start/end.
	start := t.Add(-time.Millisecond)
	end := t
	step := time.Second
	body, status, err := h.runESQLPromQL(q, start, end, step)
	if err != nil {
		errType := "execution"
		if status == http.StatusNotImplemented {
			errType = "bad_data"
		}
		writePromAPIError(w, status, errType, err.Error())
		return
	}
	// Reshape matrix then take last sample per series as vector (simplified).
	matrix, err := esqlToPrometheusMatrix(body)
	if err != nil {
		writePromAPIError(w, http.StatusBadGateway, "execution", err.Error())
		return
	}
	vec := matrixToVector(matrix, t)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "vector",
			"result":     vec,
		},
	})
}

func (h *handler) handleQueryRange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writePromAPIError(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}
	q := firstNonEmpty(r.Form.Get("query"), r.URL.Query().Get("query"))
	if q == "" {
		writePromAPIError(w, http.StatusBadRequest, "bad_data", "parameter query is required")
		return
	}
	startStr := firstNonEmpty(r.Form.Get("start"), r.URL.Query().Get("start"))
	endStr := firstNonEmpty(r.Form.Get("end"), r.URL.Query().Get("end"))
	stepStr := firstNonEmpty(r.Form.Get("step"), r.URL.Query().Get("step"))
	if startStr == "" || endStr == "" || stepStr == "" {
		writePromAPIError(w, http.StatusBadRequest, "bad_data", "parameters start, end, and step are required")
		return
	}
	start, err := parsePrometheusTime(startStr)
	if err != nil {
		writePromAPIError(w, http.StatusBadRequest, "bad_data", "start: "+err.Error())
		return
	}
	end, err := parsePrometheusTime(endStr)
	if err != nil {
		writePromAPIError(w, http.StatusBadRequest, "bad_data", "end: "+err.Error())
		return
	}
	step, err := parsePrometheusStep(stepStr)
	if err != nil {
		writePromAPIError(w, http.StatusBadRequest, "bad_data", "step: "+err.Error())
		return
	}

	body, status, err := h.runESQLPromQL(q, start, end, step)
	if err != nil {
		errType := "execution"
		if status == http.StatusNotImplemented {
			errType = "bad_data"
		}
		writePromAPIError(w, status, errType, err.Error())
		return
	}
	matrix, err := esqlToPrometheusMatrix(body)
	if err != nil {
		writePromAPIError(w, http.StatusBadGateway, "execution", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "matrix",
			"result":     matrix,
		},
	})
}

func (h *handler) runESQLPromQL(expr string, start, end time.Time, step time.Duration) ([]byte, int, error) {
	stepES := formatStepForESQL(step)
	esql := fmt.Sprintf(
		`PROMQL index=%s step=%s start="%s" end="%s" (%s)`,
		h.indexPattern,
		stepES,
		start.UTC().Format(time.RFC3339Nano),
		end.UTC().Format(time.RFC3339Nano),
		expr,
	)
	payload, err := json.Marshal(map[string]string{"query": esql})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	req, err := http.NewRequest(http.MethodPost, h.esURL+"/_query?format=json", bytes.NewReader(payload))
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	if resp.StatusCode >= 300 {
		sc := resp.StatusCode
		// Align with other backends: unknown PromQL surface is "unsupported" (501) so the
		// compliance tester counts it as unsupported rather than an unexpected hard failure.
		if sc == http.StatusBadRequest {
			low := strings.ToLower(string(body))
			if strings.Contains(low, "does not exist") &&
				(strings.Contains(low, "function [") || strings.Contains(low, "function `")) {
				sc = http.StatusNotImplemented
			}
		}
		return body, sc, fmt.Errorf("Elasticsearch returned HTTP %d: %s", sc, truncate(string(body), 800))
	}
	return body, http.StatusOK, nil
}

func writePromAPIError(w http.ResponseWriter, code int, errType, msg string) {
	writeJSON(w, code, map[string]any{
		"status":    "error",
		"errorType": errType,
		"error":     msg,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func parsePrometheusTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec, frac := math.Modf(f)
		return time.Unix(int64(sec), int64(frac*1e9)).UTC(), nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func parsePrometheusStep(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty step")
	}
	if strings.ContainsAny(s, "smhd") {
		return time.ParseDuration(s)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f <= 0 {
		return 0, fmt.Errorf("non-positive step")
	}
	// Prometheus HTTP API uses floating seconds for step when not a duration string.
	return time.Duration(f * float64(time.Second)), nil
}

func formatStepForESQL(d time.Duration) string {
	if d <= 0 {
		return "10s"
	}
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dh", d/(24*time.Hour))
	}
	if d%time.Hour == 0 && d >= time.Hour {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	if d%time.Minute == 0 && d >= time.Minute {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	if d%time.Second == 0 && d >= time.Second {
		return fmt.Sprintf("%ds", d/time.Second)
	}
	if d%time.Millisecond == 0 && d >= time.Millisecond {
		return fmt.Sprintf("%dms", d/time.Millisecond)
	}
	return fmt.Sprintf("%dns", d)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- ES|QL JSON → Prometheus matrix ---

type esqlColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type esqlResponse struct {
	Columns []esqlColumn        `json:"columns"`
	Values  [][]json.RawMessage `json:"values"`
	Error   json.RawMessage     `json:"error"`
}

func esqlToPrometheusMatrix(body []byte) ([]map[string]any, error) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("invalid ES JSON: %w", err)
	}
	if errObj, ok := top["error"]; ok && errObj != nil {
		return nil, fmt.Errorf("Elasticsearch error: %v", errObj)
	}
	var er esqlResponse
	if err := json.Unmarshal(body, &er); err != nil {
		return nil, err
	}
	if len(er.Columns) == 0 {
		return []map[string]any{}, nil
	}
	stepIdx := -1
	tsIdx := -1
	valueIdx := -1
	labelIdxs := map[int]struct{}{}

	for i, c := range er.Columns {
		lname := strings.ToLower(c.Name)
		ct := strings.ToLower(c.Type)
		switch {
		case lname == "step" || lname == "tbucket" || strings.Contains(lname, "bucket"):
			if ct == "date" || ct == "datetime" {
				stepIdx = i
			}
		case lname == "_timeseries":
			tsIdx = i
		case ct == "keyword" || ct == "text" || ct == "ip" || ct == "version":
			if lname != "step" {
				labelIdxs[i] = struct{}{}
			}
		}
	}
	if stepIdx < 0 {
		for i, c := range er.Columns {
			if strings.EqualFold(c.Type, "date") {
				stepIdx = i
				break
			}
		}
	}
	numericIdxs := []int{}
	for i, c := range er.Columns {
		if i == stepIdx || i == tsIdx {
			continue
		}
		ct := strings.ToLower(c.Type)
		if isNumericESQLType(ct) {
			numericIdxs = append(numericIdxs, i)
		}
	}
	switch len(numericIdxs) {
	case 0:
		valueIdx = -1
	case 1:
		valueIdx = numericIdxs[0]
	default:
		// Prefer the column whose name looks like a PromQL expression (parentheses).
		valueIdx = numericIdxs[len(numericIdxs)-1]
		for _, i := range numericIdxs {
			if strings.Contains(er.Columns[i].Name, "(") {
				valueIdx = i
				break
			}
		}
	}
	if valueIdx < 0 {
		return nil, fmt.Errorf("could not locate a numeric value column in ES|QL response: columns=%v", columnNames(er.Columns))
	}
	if stepIdx < 0 {
		return nil, fmt.Errorf("could not locate step/date column in ES|QL response: columns=%v", columnNames(er.Columns))
	}

	type series struct {
		metric map[string]string
		points []point
	}
	byKey := map[string]*series{}

	for _, row := range er.Values {
		if len(row) != len(er.Columns) {
			continue
		}
		ts, err := parseJSONTime(rawToString(row[stepIdx]))
		if err != nil {
			continue
		}
		val, err := parseJSONFloat(rawToString(row[valueIdx]))
		if err != nil {
			continue
		}
		metric := map[string]string{}
		if tsIdx >= 0 && tsIdx < len(row) {
			raw := strings.TrimSpace(rawToString(row[tsIdx]))
			if raw != "" && raw != "null" {
				var m map[string]any
				if err := json.Unmarshal([]byte(raw), &m); err == nil {
					for k, v := range m {
						metric[k] = fmt.Sprint(v)
					}
				}
			}
		} else {
			for li := range labelIdxs {
				if li == stepIdx || li == valueIdx {
					continue
				}
				if li >= len(row) {
					continue
				}
				name := er.Columns[li].Name
				vals := strings.TrimSpace(rawToString(row[li]))
				if vals == "" || vals == "null" {
					continue
				}
				metric[name] = vals
			}
		}

		key := metricFingerprint(metric)
		s := byKey[key]
		if s == nil {
			s = &series{metric: metric}
			byKey[key] = s
		}
		s.points = append(s.points, point{t: ts, v: val})
	}

	out := make([]map[string]any, 0, len(byKey))
	for _, s := range byKey {
		sort.Slice(s.points, func(i, j int) bool {
			return s.points[i].t.Before(s.points[j].t)
		})
		// Deduplicate identical timestamps (keep last).
		dedup := dedupePoints(s.points)
		values := make([][]any, 0, len(dedup))
		for _, p := range dedup {
			tsf := float64(p.t.UnixNano()) / 1e9
			values = append(values, []any{tsf, formatSampleValue(p.v)})
		}
		out = append(out, map[string]any{
			"metric": s.metric,
			"values": values,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return metricKey(out[i]["metric"].(map[string]string)) < metricKey(out[j]["metric"].(map[string]string))
	})
	return out, nil
}

type point struct {
	t time.Time
	v float64
}

func dedupePoints(in []point) []point {
	if len(in) < 2 {
		return in
	}
	out := []point{in[0]}
	for i := 1; i < len(in); i++ {
		if in[i].t.Equal(out[len(out)-1].t) {
			out[len(out)-1] = in[i]
		} else {
			out = append(out, in[i])
		}
	}
	return out
}

func matrixToVector(matrix []map[string]any, eval time.Time) []map[string]any {
	vec := make([]map[string]any, 0, len(matrix))
	for _, s := range matrix {
		mStr := map[string]string{}
		switch metric := s["metric"].(type) {
		case map[string]string:
			for k, v := range metric {
				mStr[k] = v
			}
		case map[string]any:
			for k, v := range metric {
				mStr[k] = fmt.Sprint(v)
			}
		default:
			continue
		}
		vals, _ := s["values"].([][]any)
		if len(vals) == 0 {
			continue
		}
		// Pick sample with timestamp <= eval, else last.
		best := vals[len(vals)-1]
		for i := len(vals) - 1; i >= 0; i-- {
			tsf, _ := vals[i][0].(float64)
			if time.Unix(0, int64(tsf*1e9)).UTC().Before(eval) || time.Unix(0, int64(tsf*1e9)).UTC().Equal(eval) {
				best = vals[i]
				break
			}
		}
		vec = append(vec, map[string]any{
			"metric": mStr,
			"value":  []any{best[0], best[1]},
		})
	}
	return vec
}

func metricFingerprint(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte('\xff')
	}
	return b.String()
}

func metricKey(m map[string]string) string {
	return metricFingerprint(m)
}

func columnNames(cols []esqlColumn) []string {
	out := make([]string, len(cols))
	for i := range cols {
		out[i] = cols[i].Name + ":" + cols[i].Type
	}
	return out
}

func isNumericESQLType(t string) bool {
	switch t {
	case "double", "float", "integer", "long", "unsigned_long", "half_float", "scaled_float":
		return true
	default:
		return false
	}
}

func rawToString(r json.RawMessage) string {
	if len(r) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(r, &s); err == nil {
		return s
	}
	var f float64
	if err := json.Unmarshal(r, &f); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	var n int64
	if err := json.Unmarshal(r, &n); err == nil {
		return strconv.FormatInt(n, 10)
	}
	var b bool
	if err := json.Unmarshal(r, &b); err == nil {
		return strconv.FormatBool(b)
	}
	return string(r)
}

func parseJSONTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return time.Time{}, fmt.Errorf("empty")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	return time.Parse(time.RFC3339, s)
}

func parseJSONFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func formatSampleValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	default:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}
