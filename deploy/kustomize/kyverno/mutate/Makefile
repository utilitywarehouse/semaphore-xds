
KYVERNO = reg.kyverno.io/kyverno/kyverno-cli:latest


.PHONY: test
test:
	docker run -v "${PWD}":"${PWD}" --workdir="${PWD}" ${KYVERNO} test "${PWD}/test"
