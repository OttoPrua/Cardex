# Install Cardex and its dispatch skill without changing provider or shell config.
[CmdletBinding()]
param(
    [ValidateSet('codex', 'hermes')][string]$Manager = 'codex',
    [string]$Version = '',
    [string]$ArchiveDir = '',
    [string]$SkillDir = ''
)
$ErrorActionPreference = 'Stop'
$Version = $Version -replace '^v', ''
if ($Version -and $Version -notmatch '^[0-9A-Za-z.+_-]+$') { throw 'Invalid version' }
$machineArch = $env:PROCESSOR_ARCHITEW6432
if (-not $machineArch) { $machineArch = $env:PROCESSOR_ARCHITECTURE }
switch ($machineArch) {
    'ARM64' { $arch = 'arm64' }
    'AMD64' { $arch = 'amd64' }
    default { throw "Unsupported Windows architecture: $machineArch" }
}
$base = 'https://github.com/OttoPrua/Cardex/releases/latest/download'
if ($Version) { $base = "https://github.com/OttoPrua/Cardex/releases/download/v$Version" }
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$tmp = Join-Path ([IO.Path]::GetTempPath()) ('cardex-install-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($tmp)
$stagedBinary = $null
# .NET hashing works in both Windows PowerShell and PowerShell 7, even when
# inherited module search paths do not expose Get-FileHash.
function Get-SHA256([string]$Path) {
    $stream = [IO.File]::OpenRead($Path)
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { return [BitConverter]::ToString($hasher.ComputeHash($stream)).Replace('-', '') }
    finally { $hasher.Dispose(); $stream.Dispose() }
}
try {
    function Get-Asset([string]$Name) {
        $destination = Join-Path $tmp $Name
        if ($ArchiveDir) { Copy-Item -LiteralPath (Join-Path $ArchiveDir $Name) -Destination $destination }
        else { Invoke-WebRequest -UseBasicParsing -Uri "$base/$Name" -OutFile $destination }
    }
    Get-Asset 'SHA256SUMS'
    $entries = @(Get-Content -LiteralPath (Join-Path $tmp 'SHA256SUMS') | ForEach-Object {
        if ($_ -match '^([a-fA-F0-9]{64})\s+(cardex_[0-9A-Za-z.+_-]+)$') {
            [PSCustomObject]@{ Hash = $Matches[1]; Name = $Matches[2] }
        }
    })
    if ($Version) { $asset = "cardex_${Version}_windows_${arch}.zip" }
    else {
        $candidates = @($entries | Where-Object { $_.Name.EndsWith("_windows_${arch}.zip") })
        if ($candidates.Count -ne 1) { throw 'Expected exactly one matching release archive in SHA256SUMS' }
        $asset = $candidates[0].Name
    }
    $matched = @($entries | Where-Object { $_.Name -eq $asset })
    if ($matched.Count -ne 1) { throw 'Missing or duplicate SHA256SUMS entry' }
    Get-Asset $asset
    $archive = Join-Path $tmp $asset
    if ((Get-SHA256 $archive) -ne $matched[0].Hash) {
        throw 'SHA256 mismatch; installation was not changed'
    }
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($archive)
    try {
        # Select exact entries; archive paths cannot write outside staging.
        foreach ($pair in @(@('cardex.exe', 'cardex.exe'), @('skills/cardex-dispatch/SKILL.md', 'SKILL.md'))) {
            $entry = $zip.GetEntry($pair[0])
            if (-not $entry -or $entry.Length -eq 0) { throw "Release is missing $($pair[0])" }
            [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, (Join-Path $tmp $pair[1]))
        }
    } finally { $zip.Dispose() }
    $binDir = Join-Path $env:LOCALAPPDATA 'Cardex\bin'
    if ($SkillDir) { $skillRoot = $SkillDir }
    elseif ($Manager -eq 'codex') { $skillRoot = Join-Path $env:USERPROFILE '.agents\skills' }
    else {
        $managerHome = $env:HERMES_HOME
        if (-not $managerHome) { $managerHome = Join-Path $env:USERPROFILE '.hermes' }
        $skillRoot = Join-Path $managerHome 'skills'
    }
    $skillDir = Join-Path $skillRoot 'cardex-dispatch'
    $backupDir = Join-Path $env:LOCALAPPDATA 'Cardex\backups'
    $suffix = 'backup-' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
    [void][IO.Directory]::CreateDirectory($binDir)
    [void][IO.Directory]::CreateDirectory($skillRoot)
    $binary = Join-Path $binDir 'cardex.exe'
    $stagedBinary = Join-Path $binDir ('.cardex-install-' + [guid]::NewGuid().ToString('N') + '.exe')
    Copy-Item -LiteralPath (Join-Path $tmp 'cardex.exe') -Destination $stagedBinary
    if (Test-Path -LiteralPath $binary) {
        try {
            # Replacement is atomic; a running/locked executable must be stopped by the user.
            [IO.File]::Replace($stagedBinary, $binary, "$binary.$suffix")
        } catch {
            throw "Cannot replace Cardex (it may be running). Close Cardex and retry; the existing binary is preserved. $($_.Exception.Message)"
        }
        Write-Host "Previous binary: $binary.$suffix"
    } else { [IO.File]::Move($stagedBinary, $binary) }
    $newSkill = Join-Path $tmp 'SKILL.md'
    $sameSkill = $false
    if (Test-Path -LiteralPath $skillDir) {
        $oldSkill = Join-Path $skillDir 'SKILL.md'
        $files = @(Get-ChildItem -LiteralPath $skillDir -Force)
        if ((Test-Path -LiteralPath $oldSkill -PathType Leaf) -and $files.Count -eq 1) {
            $sameSkill = (Get-SHA256 $oldSkill) -eq (Get-SHA256 $newSkill)
        }
        if (-not $sameSkill) {
            [void][IO.Directory]::CreateDirectory($backupDir)
            $skillBackup = Join-Path $backupDir "cardex-dispatch-$Manager.$suffix"
            Move-Item -LiteralPath $skillDir -Destination $skillBackup
            Write-Host "Previous skill: $skillBackup"
        }
    }
    if (-not $sameSkill) {
        [void][IO.Directory]::CreateDirectory($skillDir)
        Copy-Item -LiteralPath $newSkill -Destination (Join-Path $skillDir 'SKILL.md')
    }
    Write-Host "`nInstalled: $binary"
    Write-Host "Dispatch skill: $skillDir"
    Write-Host 'For this terminal, run: $env:PATH = "$env:LOCALAPPDATA\Cardex\bin;$env:PATH"'
    Write-Host "In your $Manager management session, ask: Use cardex-dispatch to guide my subscriptions and recommend a dispatch preset.`n"
    & $binary setup -inventory
    if ($LASTEXITCODE -ne 0) { throw "Cardex installed, but inventory exited with code $LASTEXITCODE" }
} finally {
    if ($stagedBinary -and (Test-Path -LiteralPath $stagedBinary)) { Remove-Item -LiteralPath $stagedBinary -Force }
    Remove-Item -LiteralPath $tmp -Recurse -Force
}
