#! /bin/bash

# hosts="
#     glusterfs-1.deeproute.ai
#     glusterfs-2.deeproute.ai
#     glusterfs-3.deeproute.ai
# "

hosts="
    k1
    k2
    k3
"
disks="
    /dev/vdb
    /dev/vdc
"

for disk in $disks; do \
    for h in $hosts; do \
        ssh $h "rm -rf /dev/ceph-*; rm -rf /var/lib/rook*; rm -rf /dev/mapper/ceph--*; dmsetup ls | grep ceph | cut -f1 | xargs dmsetup remove; wipefs -a -f $disk;"
    done
done


##!/usr/bin/env bash
#DISK="/dev/sdb"

## Zap the disk to a fresh, usable state (zap-all is important, b/c MBR has to be clean)

## You will have to run this step for all disks.
#sgdisk --zap-all $DISK

## Clean hdds with dd
#dd if=/dev/zero of="$DISK" bs=1M count=100 oflag=direct,dsync

## Clean disks such as ssd with blkdiscard instead of dd
#blkdiscard $DISK

## These steps only have to be run once on each node
## If rook sets up osds using ceph-volume, teardown leaves some devices mapped that lock the disks.
#ls /dev/mapper/ceph-* | xargs -I% -- dmsetup remove %

## ceph-volume setup can leave ceph-<UUID> directories in /dev and /dev/mapper (unnecessary clutter)
#rm -rf /dev/ceph-*
#rm -rf /dev/mapper/ceph--*

## Inform the OS of partition table changes
#partprobe $DISK
