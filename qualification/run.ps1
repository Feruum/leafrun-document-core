$ErrorActionPreference='Stop'
$PSNativeCommandUseErrorActionPreference=$true
$env:GOTOOLCHAIN='go1.27.0'
$env:GOWORK='off'
$root=(Get-Location).Path
$core=Join-Path $root 'core'
$coreSha='fd7785e2ab0caab149eeb02ef1ea753804c64be8'
git init -q $core
git -C $core fetch -q https://gitlab.com/leafrun/leafrun-document-core.git $coreSha
if((git -C $core rev-parse FETCH_HEAD) -ne $coreSha){throw 'Unexpected Core commit'}
git -C $core checkout --detach -q $coreSha
$env:GOPROXY='https://proxy.golang.org,direct'
$env:GOSUMDB='sum.golang.org'
$env:GOMODCACHE=Join-Path $root 'standalone-module-cache'
$env:GOFLAGS='-mod=readonly'
Push-Location $core
try {
    $dep=go list -m -json gitlab.com/leafrun/leafrun-integrations | ConvertFrom-Json
    if($dep.Replace -or $dep.Version -ne 'v0.1.1-0.20261106220429-8f1330455c3c'){throw 'unexpected module selection'}
    if((go env GOWORK) -ne 'off'){throw 'workspace enabled'}
    go mod download
    go mod verify
    if($IsLinux){bash scripts/ci-linux.sh; $receipt='.cache/ci-linux/receipt.json'}
    else{& ./scripts/ci-native.ps1; $receipt='.cache/ci-native/receipt.json'}
    $report=Get-Content -Raw $receipt|ConvertFrom-Json
    if(!$report.passed -or $report.revision -ne $coreSha){throw 'missing matching success receipt'}
    $receiptText=Get-Content -Raw $receipt
    $receiptText|Add-Content $env:GITHUB_STEP_SUMMARY
    Write-Output "PASS standalone main $coreSha; public proxy, GOWORK=off, canonical module and real PDF."
} finally {Pop-Location}
