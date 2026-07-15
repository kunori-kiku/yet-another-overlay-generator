param(
    [Parameter(Mandatory = $true)]
    [string]$AssetDirectory,

    [Parameter(Mandatory = $true)]
    [string]$ReleaseTag
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if ($ReleaseTag -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(preview|beta|rc)\.(0|[1-9][0-9]*))?$') {
    throw "Unsupported release tag: $ReleaseTag"
}

function Assert-SafeZip {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string[]]$RequiredFiles
    )

    $outer = Get-Item -LiteralPath $Path
    if ($outer.Attributes -band [System.IO.FileAttributes]::ReparsePoint -or
        $outer.Length -le 0 -or $outer.Length -gt 536870912) {
        throw "Outer ZIP is a reparse point, empty, or exceeds 512 MiB: $Path"
    }
    $archive = [System.IO.Compression.ZipFile]::OpenRead($Path)
    try {
        if ($archive.Entries.Count -eq 0 -or $archive.Entries.Count -gt 100000) {
            throw "ZIP member count is outside the safe bound: $($archive.Entries.Count)"
        }
        $members = [System.Collections.Generic.Dictionary[string, string]]::new([System.StringComparer]::OrdinalIgnoreCase)
        $kinds = [System.Collections.Generic.Dictionary[string, string]]::new([System.StringComparer]::OrdinalIgnoreCase)
        $totalSize = [int64]0
        foreach ($entry in $archive.Entries) {
            $raw = $entry.FullName
            if ([string]::IsNullOrEmpty($raw) -or $raw -match '[^\x20-\x7e]' -or $raw.Contains('\') -or
                $raw.StartsWith('/') -or $raw -match '^[A-Za-z]:' -or $raw.Contains('//')) {
                throw "Unsafe ZIP member name: $raw"
            }
            $isDirectory = $raw.EndsWith('/')
            $name = if ($isDirectory) { $raw.Substring(0, $raw.Length - 1) } else { $raw }
            if ([string]::IsNullOrEmpty($name)) {
                throw "ZIP root records are not allowed"
            }
            foreach ($segment in $name.Split('/')) {
                if ([string]::IsNullOrEmpty($segment) -or $segment -eq '.' -or $segment -eq '..') {
                    throw "Unsafe ZIP path segment in: $raw"
                }
            }

            $unixMode = (($entry.ExternalAttributes -shr 16) -band 0xffff)
            $unixType = ($unixMode -band 0xf000)
            if ($isDirectory) {
                if ($unixType -ne 0 -and $unixType -ne 0x4000) {
                    throw "Directory ZIP member has a special Unix type: $raw"
                }
                $kind = 'directory'
            }
            else {
                if ($unixType -ne 0 -and $unixType -ne 0x8000) {
                    throw "File ZIP member is a symlink or special type: $raw"
                }
                $kind = 'file'
            }
            if ($members.ContainsKey($name)) {
                throw "Duplicate or case-fold-colliding ZIP members: $($members[$name]) and $name"
            }
            $members.Add($name, $name)
            $kinds.Add($name, $kind)
            if ($entry.Length -lt 0 -or $entry.Length -gt 268435456) {
                throw "Unsafe ZIP member size for $raw"
            }
            $totalSize += $entry.Length
            if ($totalSize -gt 1073741824) {
                throw "ZIP expands beyond the safe total-size bound"
            }
        }

        foreach ($name in $members.Keys) {
            $parts = $name.Split('/')
            for ($i = 1; $i -lt $parts.Count; $i++) {
                $ancestor = [string]::Join('/', $parts[0..($i - 1)])
                if ($kinds.ContainsKey($ancestor) -and $kinds[$ancestor] -eq 'file') {
                    throw "File/directory prefix collision: $ancestor and $name"
                }
            }
        }
        foreach ($required in $RequiredFiles) {
            if (-not $kinds.ContainsKey($required) -or $kinds[$required] -ne 'file' -or $members[$required] -cne $required) {
                throw "Required regular ZIP member is missing or has the wrong case: $required"
            }
        }
    }
    finally {
        $archive.Dispose()
    }
}

function Assert-ExactVersion {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string]$Label
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Label is missing: $Path"
    }
    $output = @(& $Path version)
    if ($LASTEXITCODE -ne 0) {
        throw "$Label version command exited with code $LASTEXITCODE"
    }
    if ($output.Count -ne 1 -or $output[0] -cne $ReleaseTag) {
        throw "$Label reported '$($output -join '\n')'; expected exactly '$ReleaseTag'"
    }
}

$assetRoot = (Resolve-Path -LiteralPath $AssetDirectory).Path
$extractRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("yaog-release-windows-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $extractRoot | Out-Null

try {
    foreach ($architecture in @('amd64', '386')) {
        $bundlePath = Join-Path $assetRoot "yaog-bundle-windows-$architecture.zip"
        $standaloneAgent = Join-Path $assetRoot "yaog-agent-windows-$architecture.exe"
        foreach ($requiredPath in @($bundlePath, $standaloneAgent)) {
            if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
                throw "Missing Windows release asset: $requiredPath"
            }
        }

        Assert-SafeZip -Path $bundlePath -RequiredFiles @(
            'bin/yaog-server.exe',
            'bin/yaog-compiler.exe',
            'bin/yaog-agent.exe',
            'frontend/index.html',
            'frontend/yaog.wasm',
            'frontend/wasm_exec.js'
        )

        $targetRoot = Join-Path $extractRoot $architecture
        Expand-Archive -LiteralPath $bundlePath -DestinationPath $targetRoot
        $bundleBin = Join-Path $targetRoot 'bin'
        Assert-ExactVersion -Path (Join-Path $bundleBin 'yaog-server.exe') -Label "bundled Windows $architecture yaog-server.exe"
        Assert-ExactVersion -Path (Join-Path $bundleBin 'yaog-compiler.exe') -Label "bundled Windows $architecture yaog-compiler.exe"
        Assert-ExactVersion -Path (Join-Path $bundleBin 'yaog-agent.exe') -Label "bundled Windows $architecture yaog-agent.exe"
        Assert-ExactVersion -Path $standaloneAgent -Label "standalone yaog-agent-windows-$architecture.exe"

        $bundleAgentHash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $bundleBin 'yaog-agent.exe')).Hash
        $standaloneAgentHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $standaloneAgent).Hash
        if ($bundleAgentHash -cne $standaloneAgentHash) {
            throw "The Windows $architecture standalone agent differs from the agent in its bundle"
        }
    }

    Write-Output "Verified native Windows amd64 and 386 release binaries for $ReleaseTag"
}
finally {
    Remove-Item -LiteralPath $extractRoot -Recurse -Force -ErrorAction SilentlyContinue
}
