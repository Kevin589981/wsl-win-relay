[CmdletBinding(SupportsShouldProcess)]
param(
    [Parameter(Mandatory = $false)]
    [string]$BrokerExe = $env:WSL_WIN_RELAY_BROKER_EXE,

    [Parameter(Mandatory = $false)]
    [string]$TokenFile = $env:WSL_WIN_RELAY_ATTACH_TOKEN_FILE,

    [Parameter(Mandatory = $false)]
    [string]$Endpoint = $(if ($env:WSL_WIN_RELAY_BROKER_ENDPOINT) { $env:WSL_WIN_RELAY_BROKER_ENDPOINT } else { 'wsl-win-relay-broker' }),

    [Parameter(Mandatory = $false)]
    [string]$TaskName = 'WSL-Win-Relay-Broker',

    [Parameter(Mandatory = $false)]
    [string]$User = $(if ($env:USERDOMAIN) { "$env:USERDOMAIN\$env:USERNAME" } else { $env:USERNAME }),

    [switch]$StartNow,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'

function ConvertTo-CommandLineArgument([string]$Value) {
    if ($Value -notmatch '[\s"]') {
        return $Value
    }
    $escaped = [regex]::Replace($Value, '(\\*)"', '$1$1\"')
    $escaped = [regex]::Replace($escaped, '(\\+)$', '$1$1')
    return '"' + $escaped + '"'
}

if ($Uninstall) {
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        if ($PSCmdlet.ShouldProcess($TaskName, 'unregister scheduled task')) {
            Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        }
    }
    exit 0
}

if ([string]::IsNullOrWhiteSpace($BrokerExe)) {
    throw 'BrokerExe is required (set WSL_WIN_RELAY_BROKER_EXE or pass -BrokerExe).'
}
if ([string]::IsNullOrWhiteSpace($TokenFile)) {
    throw 'TokenFile is required (set WSL_WIN_RELAY_ATTACH_TOKEN_FILE or pass -TokenFile).'
}
if (-not (Test-Path -LiteralPath $BrokerExe -PathType Leaf)) {
    throw "Broker executable does not exist: $BrokerExe"
}
$tokenItem = Get-Item -LiteralPath $TokenFile -Force
if ($tokenItem.PSIsContainer -or (($tokenItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)) {
    throw "TokenFile must be a regular non-reparse file: $TokenFile"
}

# The broker only needs to read the token. Remove inherited entries and grant
# the selected interactive user read access so a copied task cannot widen it.
if ($PSCmdlet.ShouldProcess($TokenFile, "protect ACL for $User")) {
    & icacls.exe $TokenFile /inheritance:r /grant:r "${User}:(R)" | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to protect token file ACL: $TokenFile"
    }
}

$arguments = @(
    '-supervise',
    '-endpoint', (ConvertTo-CommandLineArgument $Endpoint),
    '-token-file', (ConvertTo-CommandLineArgument $TokenFile)
) -join ' '
$action = New-ScheduledTaskAction -Execute $BrokerExe -Argument $arguments -WorkingDirectory (Split-Path -Parent $BrokerExe)
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $User
$principal = New-ScheduledTaskPrincipal -UserId $User -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew -RestartCount 20 -RestartInterval (New-TimeSpan -Minutes 1)

if ($PSCmdlet.ShouldProcess($TaskName, 'register scheduled broker supervisor')) {
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
    if ($StartNow) {
        Start-ScheduledTask -TaskName $TaskName
    }
    Write-Output "registered $TaskName for $User"
}
