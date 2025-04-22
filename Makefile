CPU_COUNT = $(shell nproc)

.PHONY: test
test:
	go test -v -failfast -race -count=1000 -parallel=$(shell expr $(CPU_COUNT) \* 2) -timeout=1h

.PHONY: bench
bench:
	go test -benchtime=10s -benchmem -bench=. -run=\^\$
