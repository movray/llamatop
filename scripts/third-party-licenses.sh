#!/bin/sh
# Prints the license texts of everything compiled into the llamatop binary for
# the current GOOS/GOARCH: the Go runtime and standard library, and every
# dependency module. Release archives ship this as THIRD_PARTY_LICENSES.txt,
# because the MIT and BSD licenses require the notices in binary distributions.
#
#   GOOS=darwin GOARCH=arm64 sh scripts/third-party-licenses.sh > THIRD_PARTY_LICENSES.txt
set -eu

header() {
	printf '%s\n%s\n%s\n\n' "================================================================================" "$1" "================================================================================"
}

printf 'llamatop includes the following third-party software.\n\n'

# The official Go distribution ships $GOROOT/LICENSE; some distribution
# packages (e.g. Debian) drop it, then point GO_LICENSE at a copy.
golicense=${GO_LICENSE:-$(go env GOROOT)/LICENSE}
if [ ! -f "$golicense" ]; then
	echo "third-party-licenses: Go license not found at $golicense; set GO_LICENSE" >&2
	exit 1
fi
header "Go runtime and standard library ($(go env GOVERSION))"
cat "$golicense"
printf '\n\n'

go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' . | sort -u |
	while read -r path version dir; do
		lic=$(ls "$dir" | grep -iE '^(licen[cs]e|copying)' | head -n 1 || true)
		if [ -z "$lic" ]; then
			echo "third-party-licenses: no license file in $path $version" >&2
			exit 1
		fi
		header "$path $version"
		cat "$dir/$lic"
		printf '\n\n'
	done
