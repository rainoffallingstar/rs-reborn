#!/usr/bin/env bash
set -euo pipefail

if ! command -v curl >/dev/null 2>&1; then
  sudo apt-get update
  sudo apt-get install -y curl
fi

curl -L https://rig.r-pkg.org/deb/rig.gpg -o /tmp/rig.gpg
sudo install -m 0644 /tmp/rig.gpg /etc/apt/trusted.gpg.d/rig.gpg
echo "deb http://rig.r-pkg.org/deb rig main" | sudo tee /etc/apt/sources.list.d/rig.list >/dev/null
sudo apt-get update
sudo apt-get install -y r-rig

rig --version
