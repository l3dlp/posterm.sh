#!/usr/bin/env bash
# POSTERM — create the user and group the labs run as.
#
#   sudo config/create-usergroup.sh            # names from the .env (POSTERM_USER / POSTERM_GROUP), else posterm:posterm
#   sudo EXTRA_GROUPS="www-data docker" config/create-usergroup.sh
#
# Idempotent: on an existing user, it only fills in what's missing.
#
# Choices:
#   - system account (uid < 1000, no expiry, no mailbox);
#   - locked password: nobody logs in as posterm, you get in through the hub or
#     with `sudo -iu posterm`;
#   - a real shell (/bin/bash): every lab is a login shell;
#   - a real, private HOME (750): claude, codex… keep settings and logins there;
#   - extra groups (www-data by default, if it exists) to write to projects
#     served by the web server;
#   - umask 002 in the profile, like the service: what posterm creates stays
#     group-writable.
set -euo pipefail

cd "$(dirname "$0")/.." # the project root, where the .env lives
envval() { # last uncommented value of key $1 in the .env, unquoted
	[ -f .env ] || return 0
	sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" .env | tail -n1 | tr -d "\"'" | sed 's/[[:space:]]*$//'
}
# POSTERM_* first, then the pre-rename MONIA_* spelling.
NAME=${POSTERM_USER:-${MONIA_USER:-$(envval POSTERM_USER)}}
NAME=${NAME:-$(envval MONIA_USER)}
NAME=${NAME:-posterm}
GROUP=${POSTERM_GROUP:-${MONIA_GROUP:-$(envval POSTERM_GROUP)}}
GROUP=${GROUP:-$(envval MONIA_GROUP)}
GROUP=${GROUP:-$NAME}
HOME_DIR=${HOME_DIR:-/home/$NAME}
EXTRA_GROUPS=${EXTRA_GROUPS-www-data}
SHELL_BIN=/bin/bash

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo $0)" >&2; exit 1; }
[ "$NAME" != root ] || { echo "POSTERM_USER=root: nothing to create" >&2; exit 0; }
[ -x "$SHELL_BIN" ] || { echo "$SHELL_BIN not found" >&2; exit 1; }
say() { printf '\033[1m%s\033[0m %s\n' "→" "$*"; }

if getent group "$GROUP" >/dev/null; then
	say "group $GROUP: already there"
else
	groupadd --system "$GROUP"
	say "group $GROUP: created"
fi

if id "$NAME" >/dev/null 2>&1; then
	say "user $NAME: already there, filling in"
	usermod --gid "$GROUP" --shell "$SHELL_BIN" "$NAME" 2>&1 | { grep -v 'no changes' || true; }
	HOME_DIR=$(getent passwd "$NAME" | cut -d: -f6)
	if [ ! -d "$HOME_DIR" ]; then
		mkdir -p "$HOME_DIR"
		cp -rT /etc/skel "$HOME_DIR"
		chown -R "$NAME:$GROUP" "$HOME_DIR"
	fi
else
	useradd --system --gid "$GROUP" --create-home --home-dir "$HOME_DIR" \
		--shell "$SHELL_BIN" --comment "POSTERM - labs" "$NAME"
	say "user $NAME: created ($HOME_DIR)"
fi

passwd --lock "$NAME" >/dev/null
chmod 750 "$HOME_DIR"

for g in $EXTRA_GROUPS; do
	if getent group "$g" >/dev/null; then
		usermod --append --groups "$g" "$NAME"
		say "extra group: $g"
	else
		say "no group $g: skipped"
	fi
done

profile="$HOME_DIR/.profile"
if ! grep -qE 'umask 002 # (POSTERM|MONIA)' "$profile" 2>/dev/null; then
	printf '\numask 002 # POSTERM: what the labs create stays group-writable\n' >>"$profile"
	chown "$NAME:$GROUP" "$profile"
fi

echo
id "$NAME"
cat <<EOF

Next:
  1. hand the projects over to $NAME (see "Deploying the hub" in the README);
  2. in the .env: POSTERM_USER=$NAME and POSTERM_GROUP=$GROUP;
  3. install and log in the agents as that user: sudo -iu $NAME
EOF
