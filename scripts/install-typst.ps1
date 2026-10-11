$ErrorActionPreference = 'Stop'

# Release assets and SHA-256 digests from typst/typst v0.15.1.
$platform = if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { 'windows' } elseif ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::OSX)) { 'macos' } else { 'linux' }
$architecture = [Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString()
$releases = @{
    'windows-X64' = @('x86_64-pc-windows-msvc', 'zip', '19ce3551153c2fe7ee9fa2f95208310c8f4d3209fedb699e0333faf8913f6736')
    'windows-Arm64' = @('aarch64-pc-windows-msvc', 'zip', '4ab28e1b71ec3184d38d580ab797f499b6770d952b6b19167be5cea5c2662e14')
    'macos-X64' = @('x86_64-apple-darwin', 'tar.xz', '7f9fdd9584866245de9a79e0add8f9236fae6f40a8a45e2c4771ccc14db4e0fa')
    'macos-Arm64' = @('aarch64-apple-darwin', 'tar.xz', '48f62ed034aa3a7978309579ac6ca00045e2ef0da73114e8af27cfd8e74dc05a')
    'linux-X64' = @('x86_64-unknown-linux-musl', 'tar.xz', 'a6d077d0a95eed5a2eba715b2dae06be954f624ccbf85758a03f389ded33118c')
    'linux-Arm64' = @('aarch64-unknown-linux-musl', 'tar.xz', '5aa8d74a3d906e60ea12a66ac2f37f8eef1b14cbad7182a745e393a10c23dcee')
}
$release = $releases["$platform-$architecture"]
if (-not $release) { throw "Unsupported platform: $platform-$architecture" }

$typstTarget = $release[0]
$archiveName = "typst-$typstTarget.$($release[1])"
$installRoot = Join-Path (Split-Path $PSScriptRoot -Parent) '.tools'
New-Item -ItemType Directory -Path $installRoot -Force | Out-Null
$archive = Join-Path $installRoot $archiveName
Invoke-WebRequest -Uri "https://github.com/typst/typst/releases/download/v0.15.1/$archiveName" -OutFile $archive
$actualDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
if ($actualDigest -ne $release[2]) { throw "Typst archive checksum mismatch: $archiveName" }

if ($release[1] -eq 'zip') {
    Expand-Archive -LiteralPath $archive -DestinationPath $installRoot -Force
} else {
    & tar -xJf $archive -C $installRoot
    if ($LASTEXITCODE -ne 0) { throw 'Typst archive extraction failed' }
}
$binaryDirectory = Join-Path $installRoot "typst-$typstTarget"
$binaryName = if ($platform -eq 'windows') { 'typst.exe' } else { 'typst' }
$binary = Join-Path $binaryDirectory $binaryName
$version = & $binary --version
if ($LASTEXITCODE -ne 0 -or $version -notmatch '^typst 0\.15\.1(?:\s|$)') { throw "Unexpected Typst version: $version" }
$env:PATH = $binaryDirectory + [IO.Path]::PathSeparator + $env:PATH
if ($env:GITHUB_PATH) { Add-Content -LiteralPath $env:GITHUB_PATH -Value $binaryDirectory -Encoding utf8 }
Write-Output $version
