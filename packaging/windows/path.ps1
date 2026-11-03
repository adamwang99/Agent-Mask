# Adds (-Add) or removes (-Remove) a folder from the user's PATH. Used by the installer.
param([string]$Dir, [switch]$Add, [switch]$Remove)
$p = [Environment]::GetEnvironmentVariable('Path', 'User')
$parts = @(); if ($p) { $parts = $p -split ';' | Where-Object { $_ -ne '' } }
$parts = $parts | Where-Object { $_.TrimEnd('\') -ne $Dir.TrimEnd('\') }
if ($Add) { $parts += $Dir }
[Environment]::SetEnvironmentVariable('Path', ($parts -join ';'), 'User')
