$ErrorActionPreference='Stop'
$PSNativeCommandUseErrorActionPreference=$true
$env:GOTOOLCHAIN='go1.27.0'
$env:GOWORK='off'
$root=(Get-Location).Path
$core=Join-Path $root 'core'
$adapter=Join-Path $root 'adapter'
$proxy=Join-Path $root 'candidate-proxy'
$coreSha='e657a410addc4f8644c2cb9a49d4f270fb704009'
$adapterSha='8f1330455c3ca1267f82df1446087670c5dd55fe'
function Get-ReviewTree($path,$repo,$mr,$sha) {
    git init -q $path
    git -C $path fetch -q "https://gitlab.com/leafrun/leafrun-$repo.git" "refs/merge-requests/$mr/head"
    if((git -C $path rev-parse FETCH_HEAD) -ne $sha){throw 'MR head changed'}
    git -C $path checkout --detach -q $sha
}
Get-ReviewTree $core document-core 21 $coreSha
Get-ReviewTree $adapter integrations 4 $adapterSha
git -C $adapter fetch -q --tags https://gitlab.com/leafrun/leafrun-integrations.git
Push-Location $PSScriptRoot
try {
    go run -mod=readonly . $adapter $adapterSha $proxy
} finally {Pop-Location}
$env:GOPROXY=([uri]::new($proxy,[UriKind]::Absolute)).AbsoluteUri+',https://proxy.golang.org,direct'
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
    Write-Output "PASS standalone $coreSha using $adapterSha; GOWORK=off, canonical module and real PDF."
} finally {Pop-Location}
