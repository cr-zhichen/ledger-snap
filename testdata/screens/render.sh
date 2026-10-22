#!/bin/sh
# Renders src/*.html into iPhone-sized JPEG screenshots (1179x2556).
# Pass file names under src/ to render only those, e.g. 38_unionpay_repay_discount.html.
set -e
cd "$(dirname "$0")/src"
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
[ $# -gt 0 ] || set -- *.html
for f in "$@"; do
  n=${f%.html}
  "$CHROME" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=3 \
    --window-size=393,852 --screenshot="$PWD/../$n.png" "file://$PWD/$f" >/dev/null 2>&1
  sips -s format jpeg -s formatOptions 85 "../$n.png" --out "../$n.jpg" >/dev/null
  rm "../$n.png"
done
