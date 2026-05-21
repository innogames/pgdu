.PHONY: build deb clean

NAME    := pgdu
VERSION := 0.1.0
ARCH    := amd64
DEB     := $(NAME)_$(VERSION)_$(ARCH).deb

build:
	go build -trimpath -ldflags="-s -w" -o $(NAME) .

deb: build
	rm -rf debian-pkg
	install -D -m 0755 $(NAME) debian-pkg/usr/bin/$(NAME)
	mkdir -p debian-pkg/DEBIAN
	printf 'Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Matthias Dötsch <matthias.doetsch@innogames.com>\nSection: database\nPriority: optional\nDescription: PostgreSQL table and index disk usage explorer\n' \
		$(NAME) $(VERSION) $(ARCH) > debian-pkg/DEBIAN/control
	dpkg-deb --build --root-owner-group debian-pkg $(DEB)

clean:
	rm -rf $(NAME) debian-pkg $(NAME)_*.deb
