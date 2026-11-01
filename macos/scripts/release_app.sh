#!/usr/bin/env bash
# Builds Rexd.app for everyone: signed, notarized by Apple, and with the ticket stapled.
#
# APP_IDENTITY is a Developer ID Application certificate. NOTARY_PROFILE is a name saved
# with "xcrun notarytool store-credentials". Prints the zip's path and checksum.
set -euo pipefail

: "${APP_IDENTITY:?set APP_IDENTITY to a Developer ID Application certificate}"
: "${NOTARY_PROFILE:?set NOTARY_PROFILE to a name saved with xcrun notarytool store-credentials}"

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
source version.env

"$ROOT/scripts/package_app.sh"

ZIP="$ROOT/$APP_NAME-$MARKETING_VERSION.zip"
rm -f "$ZIP"
ditto -c -k --keepParent "$APP_NAME.app" "$ZIP"
xcrun notarytool submit "$ZIP" --keychain-profile "$NOTARY_PROFILE" --wait

# The zip Apple checked has no ticket in it, so the one to ship is made after stapling.
xcrun stapler staple "$APP_NAME.app"
xcrun stapler validate "$APP_NAME.app"
spctl --assess --type execute --verbose "$APP_NAME.app"
rm -f "$ZIP"
ditto -c -k --keepParent "$APP_NAME.app" "$ZIP"
shasum -a 256 "$ZIP"
