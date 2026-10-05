# Fresh-install Pi drill

**Do not run this drill on the engineering Pi's installed system.** Use a spare
Pi or a clean spare SD card and USB SSD.

1. Flash a clean 64-bit Raspberry Pi OS Lite image, boot it, and log in as the
   normal non-root operator.
2. Identify the USB SSD, then format and mount it (these are the only commands
   requiring `sudo`):

```sh
lsblk
sudo mkfs.ext4 /dev/sda1
sudo mkdir -p /mnt/pibase-ssd
sudo mount /dev/sda1 /mnt/pibase-ssd
sudo chown "$(id -un):$(id -gn)" /mnt/pibase-ssd
```

3. Clone the reviewed branch and run the pre-reboot drill:

```sh
git clone https://github.com/515woodpl-dot/datbase.git
cd datbase
git switch pibase/m6e
PIBASE_DATA_ROOT=/mnt/pibase-ssd/pibase ./scripts/pi-drill.sh
```

4. Reboot, return to the clone, and run only the persistence checks:

```sh
sudo reboot
cd ~/datbase
PIBASE_DATA_ROOT=/mnt/pibase-ssd/pibase PIBASE_DRILL_POSTREBOOT=1 ./scripts/pi-drill.sh
```

Paste back the two files created in the repository root:

```sh
pi-drill-YYYY-MM-DD.txt
pi-drill-YYYY-MM-DD-postreboot.txt
```
