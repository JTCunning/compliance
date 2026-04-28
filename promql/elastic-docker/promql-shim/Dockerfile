FROM golang:1.23-bookworm AS build

WORKDIR /src
COPY go.mod ./
COPY main.go ./
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /promql-shim .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /promql-shim /promql-shim
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/promql-shim"]
