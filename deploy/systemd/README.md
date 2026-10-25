# systemd deployment

```sh
sudo useradd --system --home /var/lib/deed --shell /usr/sbin/nologin deed
sudo install -m 0755 deed /usr/local/bin/deed
sudo install -d -m 0750 -o root -g deed /etc/deed
sudo deed keys init -out /etc/deed/kek && sudo chown deed:deed /etc/deed/kek && sudo chmod 0400 /etc/deed/kek
sudo install -m 0640 -o root -g deed deed.yaml /etc/deed/deed.yaml   # mode: production, data_dir: /var/lib/deed, kek.file: /etc/deed/kek
sudo install -m 0644 deploy/systemd/deed.service /etc/systemd/system/deed.service
sudo -u deed DEED_CONFIG=/etc/deed/deed.yaml deed admin bootstrap   # before the first start: prints a single-use setup link
sudo systemctl daemon-reload && sudo systemctl enable --now deed
```

- The data directory must be on a local filesystem (Deed refuses NFS/SMB) and must not be shared by two processes.
- Keep the KEK out of `/var/lib/deed`; back it up separately or let encrypted backups escrow it (docs/BACKUP.md).
- **LoadCredential:** instead of a KEK file readable by `deed`, use
  `LoadCredential=kek:/etc/deed/kek` with `kek: {source: credential, file: kek}` in deed.yaml.
- Native TLS on 443 needs `AmbientCapabilities=CAP_NET_BIND_SERVICE` and `CapabilityBoundingSet=CAP_NET_BIND_SERVICE`.
- Upgrades: replace the binary and restart; pending migrations run at start after a pre-migration snapshot
  (`storage.pre_migration_snapshots`). A database newer than the binary is refused.
