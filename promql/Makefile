.PHONY: docker docker-elastic-shim
docker:
	docker build . -t promql-compliance-tester:latest

docker-elastic-shim:
	docker build ./elastic-docker/promql-shim -t promql-elastic-shim:latest
