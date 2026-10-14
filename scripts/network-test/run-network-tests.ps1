param(
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$Compose = 'docker compose -f compose.network-test.yml'
$Gateway = 'phev-network-test-gateway'
$Mqtt = 'phev-network-test-mqtt'
$Vehicle = 'phev-network-test-vehicle'
$MqttNetwork = 'phev-network-test-mqtt'
$VehicleNetwork = 'phev-network-test-vehicle'

function Invoke-Compose([string]$CommandArgs) {
    Invoke-Expression "$Compose $CommandArgs"
    if ($LASTEXITCODE -ne 0) { throw "Compose command failed: $CommandArgs" }
}

function Wait-ForLog([string]$Container, [string]$Pattern, [int]$TimeoutSeconds = 30) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $previousPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            $logs = (& docker logs $Container 2>&1 | Out-String)
        } finally {
            $ErrorActionPreference = $previousPreference
        }
        if ($logs -match $Pattern) { return }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $deadline)
    throw "Timed out waiting for '$Pattern' in $Container`n$logs"
}

function Assert-ContainerRunning([string]$Container) {
    $status = docker inspect -f '{{.State.Status}}' $Container
    if ($status -ne 'running') { throw "$Container is not running: $status" }
}

function Wait-ForLogCount([string]$Container, [string]$Pattern, [int]$MinimumCount, [int]$TimeoutSeconds = 30) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $previousPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            $logs = (& docker logs $Container 2>&1 | Out-String)
        } finally {
            $ErrorActionPreference = $previousPreference
        }
        if (([regex]::Matches($logs, $Pattern)).Count -ge $MinimumCount) { return }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $deadline)
    throw "Timed out waiting for $MinimumCount occurrences of '$Pattern' in $Container`n$logs"
}

function Start-MqttSubscriber([string]$Topic) {
    $name = 'phev-network-test-subscriber'
    docker rm -f $name 2>$null | Out-Null
    $id = docker run -d --name $name --network $MqttNetwork `
        eclipse-mosquitto:2 mosquitto_sub -h mqtt -p 1883 -C 1 -W 30 -t $Topic
    if ($LASTEXITCODE -ne 0) { throw "Could not start MQTT subscriber for $Topic" }
    return $name
}

function Wait-ForHealthy([string]$Container, [int]$TimeoutSeconds = 30) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $health = docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $Container 2>$null
        if ($health -eq 'healthy' -or $health -eq 'running') { return }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $deadline)
    throw "Timed out waiting for $Container to become healthy"
}

function Get-ContainerLogs([string]$Container) {
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { return (& docker logs $Container 2>&1 | Out-String).Trim() }
    finally { $ErrorActionPreference = $previousPreference }
}

function Wait-MqttSubscriber([string]$Container, [int]$TimeoutSeconds = 25) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $status = docker inspect -f '{{.State.Status}}' $Container 2>$null
        if ($status -eq 'exited') {
            $output = Get-ContainerLogs $Container
            if (-not [string]::IsNullOrWhiteSpace($output)) { return $output.Trim() }
            throw "MQTT subscriber exited without receiving a message"
        }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $deadline)
    $output = Get-ContainerLogs $Container
    throw "Timed out waiting for MQTT subscriber message`n$output"
}

function Wait-MqttPayload([string]$Container, [string]$Payload, [int]$TimeoutSeconds = 30) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $output = Get-ContainerLogs $Container
        if ($output -match [regex]::Escape($Payload)) { return }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $deadline)
    throw "Timed out waiting for MQTT payload '$Payload'`n$output"
}

try {
    if (-not $SkipBuild) { Invoke-Compose 'build' }
    Invoke-Compose 'up -d'
    Wait-ForLog $Gateway 'PHEV client started successfully' 45
    Assert-ContainerRunning $Gateway
    Assert-ContainerRunning $Mqtt
    Assert-ContainerRunning $Vehicle

    Write-Host 'PASS: baseline gateway, MQTT, and vehicle emulator are running'

    docker network disconnect $MqttNetwork $Gateway
    Start-Sleep -Seconds 3
    Assert-ContainerRunning $Gateway
    docker network connect $MqttNetwork $Gateway
    Start-Sleep -Seconds 5
    Assert-ContainerRunning $Gateway
    Write-Host 'PASS: MQTT network partition did not stop the gateway'

    docker network disconnect $VehicleNetwork $Gateway
    Wait-ForLog $Gateway 'Read error|Connection closed|failed to connect' 15
    Assert-ContainerRunning $Gateway
    docker network connect $VehicleNetwork $Gateway
    Start-Sleep -Seconds 8
    Assert-ContainerRunning $Gateway
    Write-Host 'PASS: vehicle network partition was detected and gateway remained running'

    Invoke-Compose 'stop mqtt'
    Start-Sleep -Seconds 3
    Assert-ContainerRunning $Gateway
    Invoke-Compose 'start mqtt'
    Wait-ForHealthy $Mqtt 30
    Wait-ForLogCount $Gateway '\[MQTT\] State restoration complete' 2 30
    Assert-ContainerRunning $Gateway
    $availabilitySubscriber = Start-MqttSubscriber 'phev/available'
    $availability = Wait-MqttSubscriber $availabilitySubscriber 20
    if ($availability -ne 'online') { throw "Expected phev/available=online after MQTT recovery, got: $availability" }
    docker rm -f $availabilitySubscriber 2>$null | Out-Null

    $batterySubscriber = Start-MqttSubscriber 'phev/battery/level'
    docker run --rm --network $MqttNetwork eclipse-mosquitto:2 `
        mosquitto_pub -h mqtt -p 1883 -t 'phev/set/register/05' -m '0100'
    if ($LASTEXITCODE -ne 0) { throw 'Could not publish post-recovery register command' }
    $battery = Wait-MqttSubscriber $batterySubscriber 25
    if ($battery -ne '80') { throw "Expected post-recovery battery level 80, got: $battery" }
    docker rm -f $batterySubscriber 2>$null | Out-Null
    Write-Host 'PASS: MQTT broker stop/start restored connection, subscriptions, availability, and vehicle publishing'

    docker network disconnect $VehicleNetwork $Gateway
    Start-Sleep -Seconds 6
    Assert-ContainerRunning $Gateway
    $offlineBatterySubscriber = Start-MqttSubscriber 'phev/battery/level'
    Start-Sleep -Seconds 2
    docker run --rm --network $MqttNetwork eclipse-mosquitto:2 `
        mosquitto_pub -h mqtt -p 1883 -t 'homeassistant/status' -m 'online'
    if ($LASTEXITCODE -ne 0) { throw 'Could not publish Home Assistant birth message' }
    Wait-ForLog $Gateway '\[MQTT\] Republished cached state.*battery/level' 15
    docker rm -f $offlineBatterySubscriber 2>$null | Out-Null
    Assert-ContainerRunning $Gateway
    Write-Host 'PASS: cached battery state was restored while vehicle remained offline'

    docker network connect $VehicleNetwork $Gateway
    Start-Sleep -Seconds 8
    Assert-ContainerRunning $Gateway
    Write-Host 'PASS: vehicle network remained disconnected during cached-state restoration and was restored'

    Write-Host 'All network fault-injection checks passed.'
}
finally {
    Invoke-Compose 'down -v'
}
