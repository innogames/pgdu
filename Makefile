.PHONY: build run deb clean lint test

NAME    := pgdu
VERSION := 0.1.0
ARCH    := amd64
DEB     := $(NAME)_$(VERSION)_$(ARCH).deb

# -ldflags="-s -w" strips the symbol table (-s) and DWARF debug info (-w) for a
# smaller binary. Drop the -ldflags flag to keep symbols for debugging/delve.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(NAME) .

run:
	go run . $(ARGS)

deb: build
	rm -rf debian-pkg
	install -D -m 0755 $(NAME) debian-pkg/usr/bin/$(NAME)
	mkdir -p debian-pkg/DEBIAN
	printf 'Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Matthias Dötsch <matthias.doetsch@innogames.com>\nSection: database\nPriority: optional\nDescription: PostgreSQL table and index disk usage explorer\n' \
		$(NAME) $(VERSION) $(ARCH) > debian-pkg/DEBIAN/control
	dpkg-deb --build --root-owner-group debian-pkg $(DEB)

lint:
	golangci-lint run --fix
	go fix ./...

test:
	go test ./...

clean:
	rm -rf $(NAME) debian-pkg $(NAME)_*.deb
