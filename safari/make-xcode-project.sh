#!/bin/bash
# Run on the Mac (needs Xcode). Builds the Safari (MV2) extension and wraps it
# in a macOS app project with Apple's converter. Run once. The project
# references extension/dist/safari, so afterwards just rebuild
# (node extension/scripts/build.mjs safari) and press Run in Xcode.
set -euo pipefail
cd "$(dirname "$0")/.."
node tools/gen-icons.mjs >/dev/null
node extension/scripts/build.mjs safari
xcrun safari-web-extension-converter extension/dist/safari \
  --project-location safari \
  --app-name "Deep Discipleship Remote" \
  --bundle-identifier "local.ddremote.DeepDiscipleshipRemote" \
  --macos-only \
  --no-open \
  --force

# Without --copy-resources the project should point at extension/dist/safari,
# so rebuilding there is enough. Fail loudly if the converter copied instead.
pbx=$(find safari -name project.pbxproj -path '*Deep Discipleship Remote*' | head -1)
if [ -z "$pbx" ] || ! grep -q 'dist/safari' "$pbx"; then
  echo "error: the Xcode project doesn't reference extension/dist/safari." >&2
  echo "Rebuilt extension files would NOT be picked up; re-run this script after every change." >&2
  exit 1
fi
echo
echo "Note: the project references the files that exist now. If you add a new"
echo "file to the extension, run this script again to regenerate the project."
echo "Project created in safari/Deep Discipleship Remote/."
echo "Open it in Xcode, set Signing & Capabilities → Team for BOTH targets"
echo "(your Apple ID 'Personal Team' works), then Product → Run."
