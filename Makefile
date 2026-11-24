.PHONY: test test-go test-python build all

all: test

test: test-go test-python

test-go:
	cd go && go test -v ./...

test-python:
	cd python && .venv/bin/pytest -v

build:
	cd go && go build ./...
