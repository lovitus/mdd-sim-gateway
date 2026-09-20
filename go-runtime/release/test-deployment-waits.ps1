$ErrorActionPreference = 'Stop'
# Load function definitions only; never execute installer entry points or touch
# a real service. Exercise the actual shipped functions, not copies.
$path = Join-Path $PSScriptRoot 'install-windows-agent.ps1'
$tokens = $null; $errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
foreach ($name in @('Wait-State', 'Get-ServiceProcess', 'Wait-AgentExit', 'Stop-ExactAgent', 'Assert-RunningAgent')) {
    $definition = $ast.FindAll({param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst]}, $true) | Where-Object Name -eq $name
    if (@($definition).Count -ne 1) { throw "missing function: $name" }
    Invoke-Expression $definition.Extent.Text
}
function Assert($Value, [string]$Message) { if (-not $Value) { throw $Message } }
$serviceName = 'MddAgent'
$script:enumerations = 0; $script:processWaits = 0; $script:disposals = 0; $script:stops = 0
$script:pidValue = 41; $script:exitReady = $true; $script:stopFails = $false
$owned = [pscustomobject]@{ Handle = 456; Path = 'exact-owned-agent.exe' }
$owned | Add-Member ScriptMethod WaitForExit { param($Milliseconds) Assert ($Milliseconds -eq 45000) 'unbounded process wait'; $script:processWaits++; return $script:exitReady }
$owned | Add-Member ScriptMethod Dispose { $script:disposals++ }
$service = [pscustomobject]@{}
$service | Add-Member ScriptMethod WaitForStatus { param($State, $Timeout) Assert ($State -eq 'Stopped') 'unexpected wait state'; Assert ($Timeout.TotalSeconds -eq 45) 'unbounded service wait' }
$service | Add-Member ScriptMethod Dispose { }
function Get-CimInstance { [CmdletBinding()]param($ClassName, $Filter) Assert ($Filter -eq "Name='MddAgent'") 'wrong service'; return [pscustomobject]@{ ProcessId = $script:pidValue } }
function Get-Service { [CmdletBinding()]param($Name) Assert ($Name -eq 'MddAgent') 'wrong service'; return $service }
function Get-Process { [CmdletBinding()]param($Id, $Name) Assert ($Id -eq 41 -and -not $Name) 'must capture only the SCM PID'; $script:enumerations++; return $owned }
function Stop-Service { [CmdletBinding()]param($Name) $script:stops++; if ($script:stopFails) { throw 'injected-stop-failure' } }
Stop-ExactAgent
Assert ($script:enumerations -eq 1 -and $script:processWaits -eq 1 -and $script:disposals -eq 1) 'exact captured process was not waited/disposed once'
$script:exitReady = $false
try { Stop-ExactAgent; throw 'timeout accepted' } catch { Assert ($_.Exception.Message -match 'owned Agent process did not exit') 'wrong timeout error' }
Assert ($script:disposals -eq 2) 'timeout leaked captured handle'
$script:stopFails = $true
try { Stop-ExactAgent; throw 'stop error ignored' } catch { Assert ($_.Exception.Message -match 'injected-stop-failure') 'stop error swallowed' }
Assert ($script:disposals -eq 3) 'stop failure leaked process handle'
$script:pidValue = 0
Assert ($null -eq (Get-ServiceProcess)) 'stopped service must not enumerate other Agents'
Assert ($script:enumerations -eq 3) 'unexpected global process enumeration'
$source = Get-Content -Raw $path
Assert (-not ($source -match 'Get-Process -Name|Start-Sleep -Milliseconds 250')) 'retired global scan or tight polling returned'
Assert ($source.Contains('rollback blocked because candidate stop is unconfirmed')) 'rollback must fail closed on retained process ownership'
Write-Output 'Actual deployment wait functions: exact SCM PID, bounded native process wait, disposal and failed-stop protection passed'
