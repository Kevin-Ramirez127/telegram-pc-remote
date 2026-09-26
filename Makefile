.PHONY: build run test vet fmt chmod-scripts install uninstall start stop restart status logs reload

install:
	./install.sh

uninstall:
	rm -f $(HOME)/.local/bin/telegram-remote
	rm -f $(HOME)/.local/share/telegram-remote/scripts/bot.sh
	rm -f $(HOME)/.local/share/telegram-remote/bin/telegram-pc-remote
	@echo "kept user state in ~/.local/share/telegram-remote (.env, data/, commands/, logs)"

start:
	./scripts/bot.sh start

stop:
	./scripts/bot.sh stop

restart:
	./scripts/bot.sh restart

status:
	./scripts/bot.sh status

logs:
	./scripts/bot.sh logs

reload:
	./scripts/bot.sh reload

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