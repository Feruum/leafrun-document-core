param(
    [Parameter(Mandatory = $true)]
    [string] $CoreDirectory,
    [string] $ArtifactDirectory = (Join-Path ([System.IO.Path]::GetTempPath()) ('leafrun-source-demo-' + [Guid]::NewGuid().ToString('N')))
)

$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'This CI demonstration requires PowerShell 7.' }
Add-Type -AssemblyName System.Formats.Tar
Add-Type -AssemblyName System.IO.Compression.ZipFile
$coreRoot = (Resolve-Path -LiteralPath $CoreDirectory).Path
$demoRoot = [System.IO.Path]::GetFullPath($ArtifactDirectory)
if (Test-Path -LiteralPath $demoRoot) { throw 'The demonstration needs a new artifact directory.' }
[System.IO.Directory]::CreateDirectory($demoRoot) | Out-Null
$repository = Join-Path $demoRoot 'repository'
$invoice = Join-Path $repository 'invoice'
[System.IO.Directory]::CreateDirectory($repository) | Out-Null
Copy-Item -LiteralPath (Join-Path $coreRoot 'examples/invoice') -Destination $invoice -Recurse
$generatedOutput = Join-Path $invoice 'output'
if (Test-Path -LiteralPath $generatedOutput) {
    $resolvedOutput = (Resolve-Path -LiteralPath $generatedOutput).Path
    if (-not $resolvedOutput.StartsWith($demoRoot + [System.IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'The copied generated output escaped the demonstration directory.'
    }
    Remove-Item -LiteralPath $resolvedOutput -Recurse -Force
}

$zipPath = Join-Path $demoRoot 'invoice.zip'
[System.IO.Compression.ZipFile]::CreateFromDirectory($invoice, $zipPath, [System.IO.Compression.CompressionLevel]::Optimal, $true)
$tarPath = Join-Path $demoRoot 'invoice.tar.gz'
$archiveFile = [System.IO.File]::Create($tarPath)
try {
    $gzip = [System.IO.Compression.GZipStream]::new($archiveFile, [System.IO.Compression.CompressionLevel]::Optimal, $true)
    try {
        $tarWriter = [System.Formats.Tar.TarWriter]::new($gzip, [System.Formats.Tar.TarEntryFormat]::Ustar, $true)
        try {
            # Use explicit portable names. TarFile's convenience writer adds
            # './' and platform PAX metadata names rejected by the source contract.
            $rootEntry = [System.Formats.Tar.UstarTarEntry]::new([System.Formats.Tar.TarEntryType]::Directory, 'invoice')
            $tarWriter.WriteEntry($rootEntry)
            $items = Get-ChildItem -LiteralPath $invoice -Recurse -Force | Sort-Object FullName
            foreach ($item in $items) {
                $logicalName = 'invoice/' + [System.IO.Path]::GetRelativePath($invoice, $item.FullName).Replace('\', '/')
                if ($item.PSIsContainer) {
                    $entry = [System.Formats.Tar.UstarTarEntry]::new([System.Formats.Tar.TarEntryType]::Directory, $logicalName)
                    $tarWriter.WriteEntry($entry)
                } else {
                    $entry = [System.Formats.Tar.UstarTarEntry]::new([System.Formats.Tar.TarEntryType]::RegularFile, $logicalName)
                    $dataStream = [System.IO.File]::OpenRead($item.FullName)
                    try {
                        $entry.DataStream = $dataStream
                        $tarWriter.WriteEntry($entry)
                    } finally {
                        $dataStream.Dispose()
                    }
                }
            }
        } finally {
            $tarWriter.Dispose()
        }
    } finally {
        $gzip.Dispose()
    }
} finally {
    $archiveFile.Dispose()
}

$emptyTemplate = Join-Path $demoRoot 'empty-template'
$emptyHooks = Join-Path $demoRoot 'empty-hooks'
$emptyAttributes = Join-Path $demoRoot 'empty-attributes'
[System.IO.Directory]::CreateDirectory($emptyTemplate) | Out-Null
[System.IO.Directory]::CreateDirectory($emptyHooks) | Out-Null
[System.IO.File]::WriteAllText($emptyAttributes, '')
$gitOptions = @('-c', 'core.autocrlf=false', '-c', 'commit.gpgSign=false', '-c', ('core.hooksPath=' + $emptyHooks), '-c', ('core.attributesFile=' + $emptyAttributes), '-c', ('core.excludesFile=' + $emptyAttributes), '-c', 'user.name=Leafrun Fixture', '-c', 'user.email=fixture@example.invalid')
& git @gitOptions init --initial-branch=main --template=$emptyTemplate $repository
if ($LASTEXITCODE -ne 0) { throw 'Could not initialize the isolated Git demonstration.' }
& git @gitOptions -C $repository add -- invoice
if ($LASTEXITCODE -ne 0) { throw 'Could not stage the demonstration sources.' }
& git @gitOptions -C $repository commit -m 'test: capture invoice source fixture'
if ($LASTEXITCODE -ne 0) { throw 'Could not commit the demonstration sources.' }
$commit = (& git @gitOptions -C $repository rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^([a-f0-9]{40}|[a-f0-9]{64})$') { throw 'Could not resolve the demonstration commit.' }

$binaryName = if ($IsWindows) { 'leafrun.exe' } else { 'leafrun' }
$binary = Join-Path $demoRoot $binaryName
Push-Location -LiteralPath $coreRoot
try {
    & go build -o $binary ./cmd/leafrun
    if ($LASTEXITCODE -ne 0) { throw 'Could not build the demonstration CLI.' }
} finally {
    Pop-Location
}

$outputs = Join-Path $demoRoot 'results'
[System.IO.Directory]::CreateDirectory($outputs) | Out-Null
$cases = @(
    @{ Name = 'folder'; Arguments = @('render', '--project', $invoice) },
    @{ Name = 'snapshot'; Arguments = @('render', '--project', $invoice, '--snapshot') },
    @{ Name = 'zip'; Arguments = @('render', '--archive', $zipPath, '--source-root', 'invoice') },
    @{ Name = 'tar-gzip'; Arguments = @('render', '--archive', $tarPath, '--source-root', 'invoice') },
    @{ Name = 'git'; Arguments = @('render', '--git', $repository, '--ref', $commit, '--source-root', 'invoice') }
)
$evidence = @()
foreach ($case in $cases) {
    $pdf = Join-Path $outputs ($case.Name + '.pdf')
    $diagnostics = Join-Path $outputs ($case.Name + '.stderr.txt')
    $arguments = $case.Arguments + @('--output', $pdf)
    $stdout = @(& $binary @arguments 2> $diagnostics)
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) { throw ($case.Name + ' failed: ' + (Get-Content -LiteralPath $diagnostics -Raw)) }
    if ($stdout.Count -ne 1 -or -not [System.IO.Path]::IsPathFullyQualified($stdout[0])) {
        throw ($case.Name + ' did not return exactly one absolute PDF path.')
    }
    $reportedPath = [System.IO.Path]::GetFullPath($stdout[0])
    if (-not [System.IO.File]::Exists($reportedPath) -or -not [System.IO.File]::Exists($pdf)) {
        throw ($case.Name + ' did not publish the requested output and report an existing PDF.')
    }
    $header = [System.IO.File]::ReadAllBytes($pdf)
    if ($header.Length -lt 5 -or [System.Text.Encoding]::ASCII.GetString($header, 0, 5) -cne '%PDF-') {
        throw ($case.Name + ' did not publish a PDF.')
    }
    $evidence += [pscustomobject]@{ source = $case.Name; exitCode = $exitCode; pdf = $reportedPath }
    Write-Output ($case.Name + ': PDF published successfully')
}

$entry = Join-Path $invoice 'main.typ'
$originalEntry = [System.IO.File]::ReadAllBytes($entry)
$retainedPDF = Join-Path $outputs 'snapshot.pdf'
$retainedDigest = (Get-FileHash -LiteralPath $retainedPDF -Algorithm SHA256).Hash
try {
    [System.IO.File]::WriteAllText($entry, '#leafrun_missing_symbol_for_demonstration', [System.Text.UTF8Encoding]::new($false))
    $errorLog = Join-Path $outputs 'failed-snapshot.stderr.txt'
    $failedStdout = @(& $binary render --project $invoice --snapshot --output $retainedPDF 2> $errorLog)
    $failureExit = $LASTEXITCODE
    if ($failureExit -ne 1 -or $failedStdout.Count -ne 0) { throw 'The invalid source did not fail with exit 1 and empty stdout.' }
    if ([String]::IsNullOrWhiteSpace((Get-Content -LiteralPath $errorLog -Raw))) { throw 'The failed render lost its diagnostics.' }
    if ((Get-FileHash -LiteralPath $retainedPDF -Algorithm SHA256).Hash -cne $retainedDigest) {
        throw 'The failed render changed the previous PDF.'
    }
    $evidence += [pscustomobject]@{ source = 'invalid-folder-snapshot'; exitCode = $failureExit; previousPDFPreserved = $true }
    Write-Output 'invalid source: diagnostics reported and previous PDF preserved'
} finally {
    [System.IO.File]::WriteAllBytes($entry, $originalEntry)
}

$record = [pscustomobject]@{
    go = (& go version | Out-String).Trim()
    typst = (& typst --version | Out-String).Trim()
    git = (& git --version | Out-String).Trim()
    sourceCommit = $commit
    cases = $evidence
}
$record | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $demoRoot 'evidence.json') -Encoding utf8
Write-Output ('Evidence: ' + (Join-Path $demoRoot 'evidence.json'))
