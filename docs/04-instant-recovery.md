# 04 — Instant recovery (boot off a restore point)

**Case:** a VM is down and you need *something* running now, not after a
full restore + reconfiguration cycle.

```bash
# 1. boot a transient domain straight off the point
#    (incrementals replay their spans during materialization)
openglass recover -repo ~/repo -id snap4 -name ogrectest
# recovered snap4 as transient domain ogrectest from /tmp/og-recover-snap4.raw
# stop with: sudo virsh destroy ogrectest; clean up with: rm ...

# 2. check it is up
sudo -n virsh list | grep ogrectest

# 3. tear down when the real restore is ready
sudo -n virsh destroy ogrectest
sudo -n bash -c 'rm -f /tmp/og-recover-snap4.raw /tmp/og-recover-snap4.raw.xml'
```

**What happened:** the point was materialized to a raw file and a transient
(`virsh create`, never defined) domain was started on it with a read-only
virtio disk. The domain vanishes from libvirt on destroy; only the two
temp files need manual cleanup (sudo — libvirt relabels the image).

**Limits:** the disk is read-only and the domain is unmanaged (no autostart,
no libvirt metadata). This is a bridge to get running, not the final home —
migrate to a properly restored disk afterwards. Flags: `-mem` (MiB, default
512), `-name` (default `og-recover-<id>`).
