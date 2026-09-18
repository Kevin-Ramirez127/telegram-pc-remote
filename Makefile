.PHONY: build run test vet fmt chmod-scripts

build:
	go build -o bin/telegram-pc-remote .

run: build
	./bin/telegram-pc-remote

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

chmod-scripts:
	chmod +x scripts/*.sh