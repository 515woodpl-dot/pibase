# Fresh-install Pi drill

**Do not run this drill on the engineering Pi's installed system.** Use a spare
Pi or a clean spare SD card and USB SSD.

1. Flash a clean 64-bit Raspberry Pi OS Lite image, boot it, and log in as the
   normal non-root operator.
2. Identify the USB SSD, then format and mount it. Do not guess the device: set
   `DISK` from the `lsblk` output. The size and model are printed for a final
   typed confirmation. A disk with no partition table is initialized below.
   These disk-format and mount commands are the commands requiring `sudo`:

```sh
lsblk -o NAME,SIZE,MODEL,TYPE,FSTYPE,MOUNTPOINT
DISK=/dev/sdX                 # replace with the USB SSD from lsblk
lsblk -dn -o NAME,SIZE,MODEL "$DISK"
printf 'Type the exact device path to format (%s): ' "$DISK"
read CONFIRM
[ "$CONFIRM" = "$DISK" ] || { echo 'device confirmation failed'; exit 1; }
sudo wipefs -a "$DISK"
sudo parted -s "$DISK" mklabel gpt
sudo parted -s "$DISK" mkpart primary ext4 0% 100%
case "$DISK" in /dev/nvme*|/dev/mmcblk*) PARTITION="${DISK}p1";; *) PARTITION="${DISK}1";; esac
sudo partprobe "$DISK"
sudo mkfs.ext4 "$PARTITION"
sudo mkdir -p /mnt/pibase-ssd
UUID=$(sudo blkid -s UUID -o value "$PARTITION")
echo "UUID=$UUID /mnt/pibase-ssd ext4 defaults,nofail 0 2" | sudo tee -a /etc/fstab
sudo mount /mnt/pibase-ssd
sudo chown "$(id -un):$(id -gn)" /mnt/pibase-ssd
```

If `parted` reports that the disk already has a partition table, inspect it
and use the existing data partition only after confirming it is the disposable
SSD. Do not run `mkfs` on an existing partition containing data.

3. Copy the private repository to the Pi. On an engineering checkout, create
   and copy a bundle (this does not require public GitHub credentials):

```sh
# engineering machine
git bundle create /tmp/datbase-m6e.bundle pibase/m6e
scp /tmp/datbase-m6e.bundle operator@PI_HOST:/tmp/
# Pi
command -v git >/dev/null || echo 'Install git using the image package manager before continuing.'
git clone /tmp/datbase-m6e.bundle ~/datbase
cd ~/datbase
git switch pibase/m6e
```

The image may provide Git through its package manager; install it before the
clone if `command -v git` reports nothing. Run the pre-reboot drill:

```sh
PIBASE_DATA_ROOT=/mnt/pibase-ssd/pibase ./scripts/pi-drill.sh
```

4. Reboot, return to the clone, and run only the persistence checks:

```sh
sudo reboot
cd ~/datbase
findmnt -n -o SOURCE,FSTYPE,TARGET --target /mnt/pibase-ssd
# SOURCE must be UUID=$UUID (or the UUID printed by blkid before reboot).
PIBASE_DATA_ROOT=/mnt/pibase-ssd/pibase PIBASE_DRILL_POSTREBOOT=1 ./scripts/pi-drill.sh
```

Paste back the two files created in the repository root:

```sh
pi-drill-YYYY-MM-DD.txt
pi-drill-YYYY-MM-DD-postreboot.txt
```
