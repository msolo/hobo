#!/bin/bash

function die() {
  echo "$@" >&2
  exit 1
}

set -e

if (( EUID != 0 )); then
  die "You must be root to do this."
fi

if [[ "$1" != "--overwrite-rootfs" ]]; then
  die "This program modifies the rootfs. Supply --overwrite-rootfs to confirm you know what you are doing."
fi

# Add the default insecure key for bootstrapping and fix permissions.
mkdir -p /home/hobo/.ssh
curl -o /home/hobo/.ssh/authorized_keys https://raw.githubusercontent.com/msolo/hobo/master/keys/hobo-bootstrap-insecure.pub
chmod 700 /home/hobo/.ssh /home/hobo
chown -R hobo:hobo /home/hobo
chmod 600 /home/hobo/.ssh/authorized_keys

# Add sudoer permissions so we can perform bootstrap operations.
cat << EOF > /etc/sudoers.d/hobo
# hobo is an owner of the machine and has all necessary sudo permissions.
hobo ALL=(ALL) NOPASSWD:ALL
EOF

# Disable pointless cron, but leave most intact.
chmod -x $ROOTFS/etc/cron.daily/mlocate

# Disable automatic update - we can do that ourselves.
/bin/systemctl disable apt-daily.service
/bin/systemctl disable apt-daily.timer

# Update packages
apt-get update -qq
apt-get -qq upgrade

# Maybe install useful baseline items
# apt-get -qq -y --fix-missing --no-install-recommends -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confnew install $DEB_LIST

# Start removing unnecessary files
purge-old-kernels -y --keep 1
apt-get remove linux-headers-* linux-firmware
apt-get -qqy autoremove --purge
apt-get autoclean -qqy
apt-get clean -qqy

# Truncate log files
find /var/log -type f -exec cp /dev/null {} \;
find /var/lib/apt/lists -type f -delete
find /var -name '*-old' -delete

# Prune translations
rm -rf /usr/share/man/??
rm -rf /usr/share/man/??_*

# Make sure the disk will be as small as possible by zeroing out the unused blocks.
e4defrag /dev/sda1
dd if=/dev/zero of=/zero.fill bs=1024x1024; sync; rm -r /zero.fill
