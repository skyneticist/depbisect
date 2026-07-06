#!/usr/bin/env sh
# Generates examples/demo-composer/: a small git repository with two commits that
# bump two packages, one of which (breakage 1.0.0 -> 2.0.0) breaks the check.
# Everything is offline: dependencies are served from committed package
# directories via a Composer "path" repository with Packagist disabled, so
# Composer never touches the network. A path repository (rather than an artifact
# one) needs neither a zip archiver at generation time nor PHP's zip extension
# at install time. Re-run the script any time to start fresh.
set -eu

for tool in git composer php; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
chmod -R u+w demo-composer 2>/dev/null || true
rm -rf demo-composer
mkdir -p demo-composer
ROOT=$(cd demo-composer && pwd)
APP="$ROOT/app"
mkdir -p "$APP/packages"

# Composer's post-update security audit queries the network; disable it so the
# generation step (and the bisection) stay fully offline.
export COMPOSER_NO_AUDIT=1

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

# make_pkg VENDOR/NAME VERSION NAMESPACE CLASS BODY writes one package directory
# under packages/. A Composer path package is just a directory containing a
# composer.json (name + version + a PSR-4 autoload) and the PHP source it maps
# to; the path repository's "packages/*" glob picks up every directory, so two
# directories give two resolvable versions of the same package — the offline
# analog of a real registry. The namespace is passed explicitly rather than
# derived, so the script stays portable across GNU and BSD sed.
make_pkg() {
    pkg="$1"; ver="$2"; ns="$3"; class="$4"; body="$5"
    file=$(printf '%s' "$pkg" | tr '/' '-')
    dir="$APP/packages/$file-$ver"
    mkdir -p "$dir/src"
    cat > "$dir/composer.json" <<EOF
{
    "name": "$pkg",
    "version": "$ver",
    "autoload": { "psr-4": { "Acme\\\\$ns\\\\": "src/" } }
}
EOF
    cat > "$dir/src/$class.php" <<EOF
<?php
namespace Acme\\$ns;

class $class
{
$body
}
EOF
}

# --- committed package directories: two versions of each package ---------
# breakage 2.0.0 regresses (ok() returns false); helper is harmless noise.
make_pkg acme/breakage 1.0.0 Breakage Health '    public static function ok(): bool { return true; }'
make_pkg acme/breakage 2.0.0 Breakage Health '    public static function ok(): bool { return false; } // regression'
make_pkg acme/helper 1.0.0 Helper Noop '    public static function touch(): void {}'
make_pkg acme/helper 2.0.0 Helper Noop '    public static function touch(): void {} // harmless bump'

# --- the app repository -------------------------------------------------
cat > "$APP/check.php" <<'EOF'
<?php
// Health check whose result depends on the breakage package.
require __DIR__ . '/vendor/autoload.php';

use Acme\Breakage\Health;
use Acme\Helper\Noop;

Noop::touch();
if (!Health::ok()) {
    fwrite(STDERR, "app regressed after a dependency bump\n");
    exit(1);
}
echo "ok\n";
EOF

printf 'vendor/\n' > "$APP/.gitignore"

write_composer_json() {
    cat > "$APP/composer.json" <<EOF
{
    "name": "acme/demo-app",
    "description": "Offline DepBisect Composer demo",
    "require": {
        "acme/breakage": "$1",
        "acme/helper": "$2"
    },
    "repositories": [
        { "type": "path", "url": "packages/*", "options": { "symlink": false } },
        { "packagist.org": false }
    ]
}
EOF
}

cd "$APP"
git init -q -b main

# base: working dependencies. `composer update` resolves from the committed
# path packages offline (copying them into vendor/) and writes composer.lock,
# matching what a real project commits.
write_composer_json 1.0.0 1.0.0
composer update --no-interaction --no-progress -q
git add -A
git_commit "base: working dependencies"

# bump both packages; only the breakage bump is the culprit
write_composer_json 2.0.0 2.0.0
composer update --no-interaction --no-progress -q
git add -A
git_commit "bump breakage and helper"

echo "Demo repository created at examples/demo-composer/app"
echo "The check fails at HEAD and passed one commit earlier. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-composer/app --base HEAD~1 --runs 3 -- php check.php"
echo
echo "Expected result: minimal failing set = breakage 1.0.0 -> 2.0.0"
