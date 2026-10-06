#!/bin/sh
# Prints the license texts of everything compiled into the llamatop binary for
# the current GOOS/GOARCH: the Go runtime and standard library, and every
# dependency module. Release archives ship this as THIRD_PARTY_LICENSES.txt,
# because the MIT and BSD licenses require the notices in binary distributions.
#
#   GOOS=darwin GOARCH=arm64 sh scripts/third-party-licenses.sh > THIRD_PARTY_LICENSES.txt
#
# A missing license text is a warning, so local builds work on systems whose Go
# package lacks $GOROOT/LICENSE (e.g. Debian); the output then says the text is
# missing. GO_LICENSE=<file> points at a copy of the Go license instead.
# LICENSES_STRICT=1 turns warnings into errors – the release workflow sets it.
set -eu

strict=${LICENSES_STRICT:-0}

header() {
	printf '%s\n%s\n%s\n\n' "================================================================================" "$1" "================================================================================"
}

# missing <what>: warn (or fail in strict mode) and note the gap in the output.
missing() {
	if [ "$strict" = 1 ]; then
		echo "third-party-licenses: error: $1" >&2
		exit 1
	fi
	echo "third-party-licenses: warning: $1" >&2
	printf '(license text missing in this build: %s)\n\n\n' "$1"
}

printf 'llamatop includes the following third-party software.\n\n'

# The official Go distribution ships $GOROOT/LICENSE; some distribution
# packages (e.g. Debian) drop it.
golicense=${GO_LICENSE:-$(go env GOROOT)/LICENSE}
header "Go runtime and standard library ($(go env GOVERSION))"
if [ -f "$golicense" ]; then
	cat "$golicense"
	printf '\n\n'
else
	missing "Go license not found at $golicense (set GO_LICENSE=<file>)"
fi

go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' . | sort -u |
	while read -r path version dir; do
		header "$path $version"
		lic=$(ls "$dir" 2>/dev/null | grep -iE '^(licen[cs]e|copying)' | head -n 1 || true)
		if [ -n "$lic" ]; then
			cat "$dir/$lic"
			printf '\n\n'
		else
			missing "no license file in $path $version"
		fi
	done
