#!/bin/bash -x

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
curl -s -o /home/hobo/.ssh/authorized_keys https://raw.githubusercontent.com/msolo/hobo/master/keys/hobo-bootstrap-insecure.pub
chmod 700 /home/hobo/.ssh /home/hobo
chown -R hobo:hobo /home/hobo
chmod 600 /home/hobo/.ssh/authorized_keys

# Add sudoer permissions so we can perform bootstrap operations.
cat << EOF > /etc/sudoers.d/hobo
# hobo is an owner of the machine and has all necessary sudo permissions.
hobo ALL=(ALL) NOPASSWD:ALL
EOF

# systemd dns is broken by design, kneecap it.
rm /etc/resolv.conf
ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
# fix incredibly slow sudo performance due to botched lookups
echo "127.0.0.1 $(cat /etc/hostname)" >> /etc/hosts

# Ubuntu can't be trusted. sad.
sed -i 's/^ENABLED=.*/ENABLED=0/' /etc/default/motd-news

# Disable pointless cron, but leave most intact.
chmod -x $ROOTFS/etc/cron.daily/mlocate

# Disable automatic update - we can do that ourselves.
/bin/systemctl disable apt-daily.service
/bin/systemctl disable apt-daily.timer

export DEBIAN_FRONTEND=noninteractive

# Update packages
apt-get update -qq
apt-get -qq upgrade
apt-get install -qq -y --install-recommends linux-generic-hwe-18.04

# Maybe install useful baseline items
# apt-get -qq -y --fix-missing --no-install-recommends -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confnew install $DEB_LIST

# Start removing unnecessary files
apt-get -qqy remove ntfs-3g linux-*4.15.*
apt-get -qqy remove linux-headers-* linux-firmware landscape-common ubuntu-server cloud* vim
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

swapoff /swap.img || true
cp /dev/null /swap.img

# Make sure the disk will be as small as possible by zeroing out the unused blocks.
e4defrag /dev/sda?

dd if=/dev/zero of=/zero.fill bs=1024x1024 || true
sync
rm -rf /zero.fill

e2label $(awk '$2 == "/" { print $1 }' < /proc/mounts) root
