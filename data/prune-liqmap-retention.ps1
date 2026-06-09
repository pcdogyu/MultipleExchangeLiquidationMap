param(
    [string]$DbPath = (Join-Path $PSScriptRoot "liqmap.db"),
    [int]$RetentionDays = 14,
    [switch]$SkipVacuum
)

$ErrorActionPreference = "Stop"

if ($RetentionDays -le 0) {
    throw "RetentionDays must be greater than 0."
}

if (!(Test-Path -LiteralPath $DbPath)) {
    throw "Database not found: $DbPath"
}

$pythonCommand = $null
$pythonArgs = @()
$candidateLaunchers = @(
    @{ Name = "python"; Args = @() },
    @{ Name = "py"; Args = @("-3") },
    @{ Name = "python3"; Args = @() }
)

foreach ($candidate in $candidateLaunchers) {
    $cmd = Get-Command $candidate.Name -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($cmd) {
        $pythonCommand = $cmd.Source
        $pythonArgs = $candidate.Args
        break
    }
}

if (-not $pythonCommand) {
    $pathCandidates = @(
        @{ Path = "C:\WINDOWS\py.exe"; Args = @("-3") },
        @{ Path = "C:\Python312\python.exe"; Args = @() },
        @{ Path = "C:\Python311\python.exe"; Args = @() },
        @{ Path = "C:\Program Files\Python312\python.exe"; Args = @() },
        @{ Path = "C:\Program Files\Python311\python.exe"; Args = @() },
        @{ Path = "D:\Program Files\Python312\python.exe"; Args = @() },
        @{ Path = "D:\Program Files\Python311\python.exe"; Args = @() }
    )

    foreach ($pattern in @(
        (Join-Path $env:LOCALAPPDATA "Programs\Python\Python*\python.exe"),
        (Join-Path $env:LOCALAPPDATA "Microsoft\WindowsApps\python.exe"),
        (Join-Path $env:LOCALAPPDATA "Microsoft\WindowsApps\python3.exe")
    )) {
        Get-ChildItem -Path $pattern -File -ErrorAction SilentlyContinue |
            Sort-Object FullName -Descending |
            ForEach-Object {
                $pathCandidates += @{ Path = $_.FullName; Args = @() }
            }
    }

    foreach ($candidate in $pathCandidates) {
        if (Test-Path -LiteralPath $candidate.Path) {
            $pythonCommand = $candidate.Path
            $pythonArgs = $candidate.Args
            break
        }
    }
}

if (-not $pythonCommand) {
    throw "No usable Python launcher was found. Tried: python, py -3, python3. This script uses Python's built-in sqlite3 module."
}

$config = @{
    db_path        = (Resolve-Path -LiteralPath $DbPath).Path
    retention_days = $RetentionDays
    skip_vacuum    = [bool]$SkipVacuum
} | ConvertTo-Json -Compress

$env:LIQMAP_RETENTION_CONFIG = $config

$pythonScript = @'
import json
import os
import sqlite3
import sys
import time

cfg = json.loads(os.environ["LIQMAP_RETENTION_CONFIG"])
db_path = cfg["db_path"]
retention_days = int(cfg["retention_days"])
skip_vacuum = bool(cfg["skip_vacuum"])
cutoff_ms = int((time.time() - retention_days * 86400) * 1000)

steps = [
    ("band_reports", "DELETE FROM band_reports WHERE report_ts < ?", (cutoff_ms,)),
    ("liquidation_events", "DELETE FROM liquidation_events WHERE event_ts < ? AND inserted_ts < ?", (cutoff_ms, cutoff_ms)),
    ("longest_bar_reports", "DELETE FROM longest_bar_reports WHERE report_ts < ?", (cutoff_ms,)),
    ("market_state", "DELETE FROM market_state WHERE updated_ts < ?", (cutoff_ms,)),
    ("model_liqmap_snapshots", "DELETE FROM model_liqmap_snapshots WHERE generated_at < ?", (cutoff_ms,)),
    ("market_info_snapshots", "DELETE FROM market_info_snapshots WHERE generated_at < ? AND refreshed_at < ?", (cutoff_ms, cutoff_ms)),
    ("oi_snapshots", "DELETE FROM oi_snapshots WHERE updated_ts < ?", (cutoff_ms,)),
    ("price_wall_events", "DELETE FROM price_wall_events WHERE event_ts < ? AND inserted_ts < ?", (cutoff_ms, cutoff_ms)),
    ("webdatasource_points", "DELETE FROM webdatasource_points WHERE captured_at < ?", (cutoff_ms,)),
    ("webdatasource_runs", "DELETE FROM webdatasource_runs WHERE started_at < ? AND finished_at < ?", (cutoff_ms, cutoff_ms)),
    ("webdatasource_snapshots", "DELETE FROM webdatasource_snapshots WHERE captured_at < ?", (cutoff_ms,)),
    ("telegram_send_history", "DELETE FROM telegram_send_history WHERE sent_at < ?", (cutoff_ms,)),
    ("analysis_direction_signals", "DELETE FROM analysis_direction_signals WHERE signal_ts < ? AND analysis_generated_at < ?", (cutoff_ms, cutoff_ms)),
    ("analysis_liquidation_backtest_signals", "DELETE FROM analysis_liquidation_backtest_signals WHERE signal_ts < ? AND generated_at < ?", (cutoff_ms, cutoff_ms)),
    ("webdatasource_points_orphans", "DELETE FROM webdatasource_points WHERE snapshot_id NOT IN (SELECT id FROM webdatasource_snapshots)", ()),
]

before_size = os.path.getsize(db_path)
conn = sqlite3.connect(db_path)
conn.execute("PRAGMA busy_timeout=5000")
conn.execute("PRAGMA journal_mode=DELETE")
summary = []
deleted_total = 0

try:
    conn.execute("BEGIN")
    for name, stmt, params in steps:
        try:
            cur = conn.execute(stmt, params)
        except sqlite3.OperationalError as exc:
            lowered = str(exc).lower()
            if "no such table" in lowered or "no such column" in lowered:
                summary.append((name, 0))
                continue
            raise
        deleted = cur.rowcount if cur.rowcount != -1 else 0
        deleted_total += deleted
        summary.append((name, deleted))
    conn.commit()
except Exception:
    conn.rollback()
    raise

if not skip_vacuum:
    conn.execute("VACUUM")

try:
    conn.execute("PRAGMA wal_checkpoint(PASSIVE)")
except sqlite3.DatabaseError:
    pass

conn.close()
after_size = os.path.getsize(db_path)

print(f"db_path={db_path}")
print(f"cutoff_ms={cutoff_ms}")
print(f"deleted_total={deleted_total}")
for name, deleted in summary:
    if deleted:
        print(f"{name}={deleted}")
print(f"size_before_bytes={before_size}")
print(f"size_after_bytes={after_size}")
'@

try {
    $pythonScript | & $pythonCommand @pythonArgs -
}
finally {
    Remove-Item Env:LIQMAP_RETENTION_CONFIG -ErrorAction SilentlyContinue
}
