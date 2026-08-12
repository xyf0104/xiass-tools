<#
.SYNOPSIS
Removes stale CodeStudio Lite bundle registrations left behind by interrupted
installs.

.DESCRIPTION
Every build of the installer gets a fresh bundle id, so repeatedly installing
the same version registers each build as a separate same-version sibling. Burn
cleans siblings up by running each one's own cached installer, and a cached
installer that cannot finish leaves its registration in place — which is how
these accumulate.

Only bundle registrations are removed. The Windows Installer record is what
actually owns the installed files, and it is left alone: run the product's own
uninstaller if you want the program gone.

Nothing is deleted without being listed first. Pass -Apply to act; the default
is a dry run.
#>
[CmdletBinding()]
param(
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'

$roots = @(
    'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall',
    'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall'
)

$candidates = foreach ($root in $roots) {
    if (-not (Test-Path $root)) { continue }
    Get-ChildItem $root | ForEach-Object {
        $entry = Get-ItemProperty $_.PSPath -ErrorAction SilentlyContinue
        if (-not $entry) { return }
        if ($entry.DisplayName -notlike '*CodeStudio*') { return }
        # A bundle registration carries its cached installer. The MSI record
        # carries WindowsInstaller=1 instead, and removing it would strand the
        # installed files with nothing to uninstall them.
        if ($entry.WindowsInstaller -eq 1) { return }
        if ([string]::IsNullOrWhiteSpace($entry.BundleCachePath)) { return }
        [pscustomobject]@{
            Id        = $_.PSChildName
            Version   = $entry.DisplayVersion
            KeyPath   = $_.PSPath
            CachePath = Split-Path $entry.BundleCachePath -Parent
        }
    }
}

if (-not $candidates) {
    Write-Host 'No stale bundle registrations found.' -ForegroundColor Green
    exit 0
}

Write-Host ("Found {0} bundle registration(s):" -f @($candidates).Count) -ForegroundColor Cyan
foreach ($item in $candidates) {
    Write-Host ("  {0}  version {1}" -f $item.Id, $item.Version)
    Write-Host ("      key   {0}" -f ($item.KeyPath -replace '^Microsoft\.PowerShell\.Core\\Registry::', ''))
    Write-Host ("      cache {0}" -f $item.CachePath)
}

$msi = foreach ($root in $roots) {
    if (-not (Test-Path $root)) { continue }
    Get-ChildItem $root | ForEach-Object {
        $entry = Get-ItemProperty $_.PSPath -ErrorAction SilentlyContinue
        if ($entry -and $entry.DisplayName -like '*CodeStudio*' -and $entry.WindowsInstaller -eq 1) {
            $_.PSChildName
        }
    }
}
if ($msi) {
    Write-Host ''
    Write-Host ("Keeping the Windows Installer record: {0}" -f ($msi -join ', ')) -ForegroundColor Yellow
}

if (-not $Apply) {
    Write-Host ''
    Write-Host 'Dry run. Re-run with -Apply to remove them.' -ForegroundColor Yellow
    exit 0
}

foreach ($item in $candidates) {
    Write-Host ("Removing {0}" -f $item.Id)
    Remove-Item -LiteralPath $item.KeyPath -Recurse -Force
    if ($item.CachePath -and (Test-Path -LiteralPath $item.CachePath)) {
        Remove-Item -LiteralPath $item.CachePath -Recurse -Force -ErrorAction Continue
    }
}

Write-Host ''
Write-Host 'Done. Install the current package to register one current bundle.' -ForegroundColor Green
