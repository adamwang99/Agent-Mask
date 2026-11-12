# Adds (-Add) or removes (-Remove) a folder from the user's PATH. Used by the installer.
param([string]$Dir, [switch]$Add, [switch]$Remove)
$p = [Environment]::GetEnvironmentVariable('Path', 'User')
# @(...) keeps an array even with one entry: a lone string would get the folder glued onto it.
$parts = @(); if ($p) { $parts = @($p -split ';' | Where-Object { $_ -ne '' }) }
$parts = @($parts | Where-Object { $_.TrimEnd('\') -ne $Dir.TrimEnd('\') })
if ($Add) { $parts += $Dir }
[Environment]::SetEnvironmentVariable('Path', ($parts -join ';'), 'User')
