# hobo
"Vagrant on Rails"

Hobo is a simple VM template manager for VMWare Fusion. It works for OS X/Linux VMs, though anything that can be controlled via SSH can be made to work easily.

Hobo is intentionally simple and designed to give you a reliable VM without network latency. It doesn't try to pretend to be anything more sophisticated than that. There are no special mounting/sharing options and only a simple rootfs upgrade path.

# Boxcars

A boxcar is just a tarball of vmwarevm directory. It should follow some basic conventions to keep things flexible and useful.
* It must contain a single top-level directory named <boxcar name>.vmwarevm
* It must contain a root disk <boxcar name>.vmwarevm/root.vmdk
* It may contain a home disk <boxcar name>.vmwarevm/home.vmdk
  * The home.vmdk will be preserved across upgrades of a vm. This is very useful if changes are made to the base image, or users are in the habit of making otherwise unrecoverable changes to the root filesystem.

# Hobo Boxcar Creation

## Create A New Linux VM with VMWare Fusion
1. Create a new VM with <boxcar name>.
   * 4 vCPU, 8GB RAM
   * Add 50GB root.vmdk (obviously the sizes are suggestions)
   * Add 100GB home.vmdk (any changes to the home disk are ignored and require manual migration)
2. Install OS to /dev/sda1 with label=rootfs
   * In this example, I used Ubuntu 16.04 LTS
   * Set username/password: hobo/hobo
   * Set hostname: <boxcar name>
3. Create a home volume on /dev/sdb label=homefs

## Initialize A New Linux VM
    # useradd hobo -s /bin/bash
    # Must be run as root.
    mkdir -p /home/hobo/.ssh
    # Add the default insecure key for bootstrapping and fix permissions.
    curl -o /home/hobo/.ssh/authorized_keys https://raw.githubusercontent.com/msolo/hobo/master/keys/hobo-bootstrap-insecure.pub
    chmod 700 /home/hobo/.ssh /home/hobo
    chown -R hobo:hobo /home/hobo
    chmod 600 /home/hobo/.ssh/authorized_keys
    # Add sudoer permissions so we can perform bootstrap operations.
    cat << EOF > /etc/sudoers.d/hobo
    # hobo is an owner of the machine and has all necessary sudo permissions.
    hobo ALL=(ALL) NOPASSWD:ALL
    EOF
    # Place any interesting operations here - for instance:
    # Disable pointless cron, but leave most intact.
    # chmod -x $ROOTFS/etc/cron.daily/mlocate
    # Disable automatic update - we can do that ourselves.
    # /bin/systemctl disable apt-daily.service
    # /bin/systemctl disable apt-daily.timer
    # apt-get update -qq
    # apt-get -qq upgrade
    # apt-get -qq -y --allow-unauthenticated --fix-missing --no-install-recommends -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confnew install $DEB_LIST
    # apt-get remove linux-headers-* linux-firmware vim
    # purge-old-kernels -y --keep 1
    # apt-get -qqy autoremove --purge
    # apt-get autoclean -qqy
    # apt-get clean -qqy
    # truncate log files
    # find /var/log -type f -exec cp /dev/null {} \;
    # find /var/lib/apt/lists -type f -delete
    # find /var -name '*-old' -delete
    # rm -rf /usr/share/man/??
    # rm -rf /usr/share/man/??_*
    # Make sure the disk will be as small as possible by zeroing out the blocks.
    e4defrag /dev/sda1
    dd if=/dev/zero of=/zero.fill bs=1024x1024; sync; rm /zero.fill
    #cat /dev/zero > zero.fill; sync; sleep 1; sync; rm -f zero.fill

## Create Boxcar Archive
    hobo make-boxcar <boxcar name>.vmwarevm
    shasum -a 256 <boxcar name>.vmwarevm.tgz | awk '{print $1}' > <boxcar name>.vmwarevm.tgz.sha256

### Compressing further
    gunzip -k -c <boxcar name>.vmwarevm.tgz | pixz <boxcar name>.vmwarevm.txz

## Create A VM Config
Create a .hobo file that references the boxcar archive and gives it a local name.

You can use BootstrapCmdLines to run a series of bash commands inside the guest after cloning is complete. These should be idempotent, but generally hobo guarantees that these commands will only be run once.

```
{
  "Name": "demo",
  "Boxcar": {
    "Name": "demo-boxcar",
    "Url": "http://localhost/demo-boxcar.vmwarevm.tgz",
    "Version": "3",
    "Sha256": "c9d99d83b26aa17509c4e2e2f7e7f2b59c48cf9fdfffbf75ec2c35d412d87a41",
    "BootstrapCmdLines": ["echo running hobo_cmd=$HOBO_CMD for hobo_host_user=$HOBO_HOST_USER"]
  }
}
```
Then the first call to `hobo start` will fetch the boxcar archives, unpack and clone the vm and then run the bootstrap commands inside the guest OS.

You will be able to ssh into the vm afterward using `hobo ssh`.
