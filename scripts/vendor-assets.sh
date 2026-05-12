#!/usr/bin/env bash
# vendor-assets.sh — Download and pin every third-party frontend asset the
# native renderer serves under /_/static/. Idempotent: re-running it
# overwrites the existing files and regenerates MANIFEST.txt with sha256
# hashes so reviewers can spot drift.
#
# Pinned versions live as locals near the top — bumping a version is one
# line. The script intentionally has zero side effects outside the
# `internal/server/assets/` tree.

set -euo pipefail

# Locate the project root from the script path so the script works regardless
# of where it's invoked from (repo root, ./scripts, CI runner, etc.).
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
ASSETS_DIR="${REPO_ROOT}/internal/server/assets"
VENDOR_DIR="${ASSETS_DIR}/vendor"
FONTS_DIR="${ASSETS_DIR}/fonts"
KATEX_DIR="${VENDOR_DIR}/katex"

# --- Pinned versions -------------------------------------------------------
HTMX_VERSION="2.0.4"
HTMX_IDIOMORPH_VERSION="0.7.3"
ALPINE_VERSION="3.14.9"
KATEX_VERSION="0.16.11"
MERMAID_VERSION="11.4.1"

mkdir -p "${VENDOR_DIR}" "${FONTS_DIR}" "${KATEX_DIR}/fonts"

# fetch URL DEST — download URL to DEST. Fails the script on any HTTP error.
fetch() {
    local url="$1"
    local dest="$2"
    echo "  fetching ${url}"
    curl -fsSL --retry 3 --retry-delay 1 -o "${dest}" "${url}"
}

# --- htmx + idiomorph + Alpine --------------------------------------------
echo "==> htmx ${HTMX_VERSION}"
fetch "https://unpkg.com/htmx.org@${HTMX_VERSION}/dist/htmx.min.js" \
    "${VENDOR_DIR}/htmx.min.js"
fetch "https://unpkg.com/idiomorph@${HTMX_IDIOMORPH_VERSION}/dist/idiomorph-ext.min.js" \
    "${VENDOR_DIR}/htmx-idiomorph-ext.min.js"

echo "==> Alpine ${ALPINE_VERSION}"
fetch "https://cdn.jsdelivr.net/npm/alpinejs@${ALPINE_VERSION}/dist/cdn.min.js" \
    "${VENDOR_DIR}/alpine.min.js"
fetch "https://cdn.jsdelivr.net/npm/@alpinejs/persist@${ALPINE_VERSION}/dist/cdn.min.js" \
    "${VENDOR_DIR}/alpine-persist.min.js"

# --- KaTeX -----------------------------------------------------------------
echo "==> KaTeX ${KATEX_VERSION}"
fetch "https://cdn.jsdelivr.net/npm/katex@${KATEX_VERSION}/dist/katex.min.js" \
    "${KATEX_DIR}/katex.min.js"
fetch "https://cdn.jsdelivr.net/npm/katex@${KATEX_VERSION}/dist/katex.min.css" \
    "${KATEX_DIR}/katex.min.css"
fetch "https://cdn.jsdelivr.net/npm/katex@${KATEX_VERSION}/dist/contrib/auto-render.min.js" \
    "${KATEX_DIR}/auto-render.min.js"

# KaTeX needs a small set of font files to render math glyphs. We pin the
# Main + Math families in regular weight only — that's enough for ~95% of
# math in the wild. Additional families (Size variants, Caligraphic, etc.)
# would add ~150 KB; not worth it for v1.
KATEX_FONTS=(
    "KaTeX_Main-Regular.woff2"
    "KaTeX_Main-Bold.woff2"
    "KaTeX_Main-Italic.woff2"
    "KaTeX_Math-Italic.woff2"
    "KaTeX_AMS-Regular.woff2"
    "KaTeX_Size1-Regular.woff2"
    "KaTeX_Size2-Regular.woff2"
    "KaTeX_Size3-Regular.woff2"
    "KaTeX_Size4-Regular.woff2"
)
for font in "${KATEX_FONTS[@]}"; do
    fetch "https://cdn.jsdelivr.net/npm/katex@${KATEX_VERSION}/dist/fonts/${font}" \
        "${KATEX_DIR}/fonts/${font}"
done

# --- Mermaid ---------------------------------------------------------------
echo "==> Mermaid ${MERMAID_VERSION}"
fetch "https://cdn.jsdelivr.net/npm/mermaid@${MERMAID_VERSION}/dist/mermaid.min.js" \
    "${VENDOR_DIR}/mermaid.min.js"

# --- Webfonts (Google Fonts via gwfh) --------------------------------------
# google-webfonts-helper hands back a zip of woff2 files for the chosen
# subset/weight. Skipping italic to keep the bundle small; bold + regular
# are enough for content typography (Quartz today doesn't use italic either).
#
# If gwfh is unreachable, the fallback is to bake bare Google Fonts CDN
# links into wiki.css — documented in docs/RENDERER.md. We intentionally do
# not silently fall back here: a failed asset vendor should fail the script
# so the operator notices.
fetch_webfont() {
    local family="$1"
    local archive_path="$2"
    local url="https://gwfh.mranftl.com/api/fonts/${family}?download=zip&subsets=latin&formats=woff2"
    echo "==> webfont ${family}"
    if ! fetch "${url}" "${archive_path}"; then
        echo "    !! webfont fetch failed for ${family}; the renderer will fall back" >&2
        echo "    !! to Google Fonts CDN at runtime (see docs/RENDERER.md). Aborting." >&2
        exit 1
    fi
}

WEBFONT_TMP="$(mktemp -d)"
trap 'rm -rf -- "${WEBFONT_TMP}"' EXIT

for family in schibsted-grotesk source-sans-3 ibm-plex-mono; do
    fetch_webfont "${family}" "${WEBFONT_TMP}/${family}.zip"
    # Extract only the regular + bold woff2 variants; gwfh names them
    # `${family}-v##-latin-{regular,700}.woff2`.
    unzip -p -j "${WEBFONT_TMP}/${family}.zip" "*-latin-regular.woff2" \
        > "${FONTS_DIR}/${family}-regular.woff2"
    # gwfh uses "700" for bold weight. The pattern is permissive in case
    # gwfh's bold naming changes minor versions later.
    unzip -p -j "${WEBFONT_TMP}/${family}.zip" "*-latin-700.woff2" \
        > "${FONTS_DIR}/${family}-bold.woff2"
done

# --- MANIFEST --------------------------------------------------------------
echo "==> Regenerating MANIFEST.txt"
(
    cd "${ASSETS_DIR}"
    {
        echo "# sha256 manifest of all vendored assets under internal/server/assets/."
        echo "# Generated by scripts/vendor-assets.sh — do not edit by hand."
        echo "# Run scripts/vendor-assets.sh to regenerate after a version bump."
        echo ""
        find vendor fonts -type f \( -name '*.js' -o -name '*.css' -o -name '*.woff2' \) -print0 \
            | sort -z \
            | xargs -0 shasum -a 256
    } > MANIFEST.txt
)

echo "Done. Re-run with --check to verify hashes match MANIFEST.txt."
