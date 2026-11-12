<#
.SYNOPSIS
  Agente Lynx para Windows (PowerShell). Coleta métricas, portas, conexões, logs de eventos e
  envia ao servidor Lynx no mesmo formato do agente Linux.

.DESCRIPTION
  Instalar (como Administrador):
    .\lynx-agent.ps1 -Install                       # pergunta o endereço do servidor e o token
    .\lynx-agent.ps1 -Install -Server http://IP:8080 -Token SEU_TOKEN
  Desinstalar:
    .\lynx-agent.ps1 -Uninstall
  Testar sem instalar:
    .\lynx-agent.ps1 -Server http://IP:8080 -Token SEU_TOKEN -Once      # envia 1 ciclo e sai
    .\lynx-agent.ps1 -Server http://IP:8080 -Token SEU_TOKEN -Once -Dump   # só mostra o JSON

  Configuração (precedência: parâmetros > variáveis AGENT_* > C:\ProgramData\Lynx\agent.json):
    server, token, name (padrão: nome da máquina), interval (segundos, padrão 15), insecure (aceita TLS inválido)

  Requisitos: Windows Server 2012 R2 / Windows 8.1 ou superior, PowerShell 5.1 (recomendado).
  O agente só faz conexões de saída para o servidor Lynx; não abre nenhuma porta.
#>
param(
  [string]$Server,
  [string]$Token,
  [string]$Name,
  [int]$Interval = 0,
  [switch]$Install,
  [switch]$Uninstall,
  [switch]$Once,
  [switch]$Dump,
  [switch]$Insecure
)

$script:Version = 'win-1.0.0'
$script:TaskName = 'LynxAgent'
[Threading.Thread]::CurrentThread.CurrentCulture = [Globalization.CultureInfo]::InvariantCulture

# ----------------------------------------------------------------------------------------------
# utilidades
# ----------------------------------------------------------------------------------------------
function Get-DataDir { Join-Path $env:ProgramData 'Lynx' }
function Get-AppDir { Join-Path $env:ProgramFiles 'Lynx' }

function Write-Log([string]$msg) {
  try {
    $dir = Get-DataDir
    if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
    $f = Join-Path $dir 'agent.log'
    if ((Test-Path $f) -and ((Get-Item $f).Length -gt 1MB)) { Move-Item $f "$f.old" -Force }
    Add-Content -Path $f -Value ('{0:yyyy-MM-dd HH:mm:ss} {1}' -f (Get-Date), $msg) -Encoding UTF8
  } catch {}
}

function R1($x) { [math]::Round([double]$x, 1) }
function Clamp([double]$x, [double]$lo, [double]$hi) { [math]::Max($lo, [math]::Min($hi, $x)) }
function Clip([string]$s, [int]$n) { if ($s.Length -gt $n) { $s.Substring(0, $n - 1) + '…' } else { $s } }
function UnixMs([datetime]$d) { [long]((([datetime]$d).ToUniversalTime().Ticks - 621355968000000000) / 10000) }

$script:PortNames = @{
  22 = 'SSH'; 25 = 'SMTP'; 53 = 'DNS'; 80 = 'HTTP'; 88 = 'Kerberos'; 110 = 'POP3'; 123 = 'NTP'; 135 = 'RPC'; 139 = 'NetBIOS'
  143 = 'IMAP'; 389 = 'LDAP'; 443 = 'HTTPS'; 445 = 'SMB'; 465 = 'SMTPS'; 514 = 'Syslog'; 587 = 'SMTP'; 636 = 'LDAPS'
  993 = 'IMAPS'; 995 = 'POP3S'; 1433 = 'SQL Server'; 1514 = 'Wazuh'; 1515 = 'Wazuh'; 1521 = 'Oracle DB'; 2049 = 'NFS'
  2181 = 'ZooKeeper'; 2375 = 'Docker API'; 2376 = 'Docker API'; 3000 = 'Grafana'; 3100 = 'Loki'; 3268 = 'LDAP GC'
  3306 = 'MySQL/MariaDB'; 3389 = 'RDP'; 5000 = 'HTTP-alt'; 5432 = 'PostgreSQL'; 5601 = 'Kibana'; 5672 = 'RabbitMQ'
  5984 = 'CouchDB'; 5985 = 'WinRM'; 5986 = 'WinRM'; 6379 = 'Redis'; 6443 = 'Kubernetes API'; 8000 = 'HTTP-alt'
  8080 = 'HTTP-alt'; 8086 = 'InfluxDB'; 8443 = 'HTTPS-alt'; 8500 = 'Consul'; 9042 = 'Cassandra'; 9090 = 'Prometheus'
  9092 = 'Kafka'; 9093 = 'Alertmanager'; 9100 = 'Node Exporter'; 9200 = 'Elasticsearch'; 9300 = 'Elasticsearch'
  9389 = 'AD Web Services'; 10050 = 'Zabbix Agent'; 10051 = 'Zabbix Server'; 11211 = 'Memcached'; 15672 = 'RabbitMQ UI'
  27017 = 'MongoDB'; 47001 = 'WinRM'; 55000 = 'Wazuh API'
}
$script:ProcHints = @{
  'sqlservr' = 'SQL Server'; 'w3wp' = 'IIS'; 'nginx' = 'Nginx'; 'httpd' = 'Apache'; 'apache' = 'Apache'; 'caddy' = 'Caddy'
  'traefik' = 'Traefik'; 'postgres' = 'PostgreSQL'; 'mysqld' = 'MySQL/MariaDB'; 'mariadbd' = 'MySQL/MariaDB'
  'redis-server' = 'Redis'; 'mongod' = 'MongoDB'; 'zabbix_agentd' = 'Zabbix Agent'; 'zabbix_agent2' = 'Zabbix Agent'
  'zabbix_server' = 'Zabbix Server'; 'sshd' = 'SSH'; 'dns' = 'DNS (Windows)'; 'grafana' = 'Grafana'
  'prometheus' = 'Prometheus'; 'windows_exporter' = 'Windows Exporter'; 'wazuh-agent' = 'Wazuh'; 'filebeat' = 'Filebeat'
  'rabbitmq-service' = 'RabbitMQ'; 'memcached' = 'Memcached'; 'tomcat9' = 'Tomcat'; 'tomcat10' = 'Tomcat'
}
$script:WebPorts = @(80, 443, 3000, 5000, 8000, 8008, 8080, 8081, 8443, 8888, 9443)

function Get-Svc([int]$port, [string]$proc, [bool]$useProc) {
  if ($useProc -and $proc) {
    $k = $proc.ToLowerInvariant()
    if ($script:ProcHints.ContainsKey($k)) { return $script:ProcHints[$k] }
  }
  if ($script:PortNames.ContainsKey($port)) { return $script:PortNames[$port] }
  return ''
}

# ----------------------------------------------------------------------------------------------
# configuração
# ----------------------------------------------------------------------------------------------
function Get-Config($pServer, $pToken, $pName, $pInterval, $pInsecure) {
  $c = @{ server = ''; token = ''; name = ''; interval = 15; insecure = $false }
  $file = Join-Path (Get-DataDir) 'agent.json'
  if (Test-Path $file) {
    try {
      $j = Get-Content -Raw -Encoding UTF8 $file | ConvertFrom-Json
      foreach ($k in 'server', 'token', 'name', 'interval', 'insecure') {
        if ($null -ne $j.$k -and "$($j.$k)" -ne '') { $c[$k] = $j.$k }
      }
    } catch { Write-Log "agent.json inválido: $($_.Exception.Message)" }
  }
  if ($env:AGENT_SERVER) { $c.server = $env:AGENT_SERVER }
  if ($env:AGENT_TOKEN) { $c.token = $env:AGENT_TOKEN }
  if ($env:AGENT_NAME) { $c.name = $env:AGENT_NAME }
  if ($env:AGENT_INTERVAL) { $c.interval = [int]$env:AGENT_INTERVAL }
  if ($pServer) { $c.server = $pServer }
  if ($pToken) { $c.token = $pToken }
  if ($pName) { $c.name = $pName }
  if ($pInterval -gt 0) { $c.interval = $pInterval }
  if ($pInsecure) { $c.insecure = $true }
  if (-not $c.name) { $c.name = [Net.Dns]::GetHostName() }
  $c.interval = [int](Clamp ([double]$c.interval) 5 300)
  $c.server = ([string]$c.server).Trim().TrimEnd('/')
  if ($c.server -and $c.server -notmatch '^https?://') { $c.server = 'http://' + $c.server }
  return $c
}

# ----------------------------------------------------------------------------------------------
# TLS tolerante (só para probes em localhost e, se pedido, para o servidor Lynx)
# ----------------------------------------------------------------------------------------------
function Enable-InsecureTls {
  if ('LynxTls' -as [type]) { return }
  Add-Type -TypeDefinition @'
using System.Net.Security;
public static class LynxTls {
  public static readonly RemoteCertificateValidationCallback Any = delegate { return true; };
}
'@
}

# ----------------------------------------------------------------------------------------------
# coletores
# ----------------------------------------------------------------------------------------------
$script:state = @{
  prevCpu = @{}; prevNet = @{}; prevT = $null; sw = [Diagnostics.Stopwatch]::StartNew()
  load = $null; static = $null; disks = @(); swap = @{ total = 0L; used = 0L }; lastSlow = [datetime]::MinValue
  listen = @(); conns = @(); probes = @(); logSince = (Get-Date).AddMinutes(-5); pendingLogs = @(); dropped = 0
  cycles = 0
}

function Get-StaticInfo {
  if ($script:state.static) { return $script:state.static }
  $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
  $cpu = @(Get-CimInstance -ClassName Win32_Processor -ErrorAction SilentlyContinue)
  $model = ''
  if ($cpu.Count -gt 0) { $model = ([string]$cpu[0].Name).Trim() -replace '\s+', ' ' }
  $arch = ([string]$env:PROCESSOR_ARCHITECTURE).ToLowerInvariant()
  if (-not $arch) { $arch = 'amd64' }
  $script:state.static = @{
    os = ([string]$os.Caption).Trim(); kernel = 'NT ' + $os.Version; arch = $arch; cpu = $model; boot = $os.LastBootUpTime
  }
  return $script:state.static
}

function Get-HostInfo {
  $s = Get-StaticInfo
  $ips = New-Object Collections.Generic.List[string]
  try {
    foreach ($n in [Net.NetworkInformation.NetworkInterface]::GetAllNetworkInterfaces()) {
      if ($n.OperationalStatus -ne 'Up' -or $n.NetworkInterfaceType -eq 'Loopback') { continue }
      foreach ($a in $n.GetIPProperties().UnicastAddresses) {
        if ($a.Address.AddressFamily -eq 'InterNetwork') {
          $ip = $a.Address.IPAddressToString
          if (-not $ip.StartsWith('169.254.')) { $ips.Add($ip) }
        }
      }
    }
  } catch {}
  $up = 0L
  if ($s.boot) { $up = [long]((Get-Date) - [datetime]$s.boot).TotalSeconds }
  return [ordered]@{
    hostname = [Net.Dns]::GetHostName(); os = $s.os; kernel = $s.kernel; arch = $s.arch
    cpu_model = $s.cpu; cores = [int][Environment]::ProcessorCount; uptime = $up; ips = $ips.ToArray()
  }
}

function Get-CpuData {
  $rows = @(Get-CimInstance -ClassName Win32_PerfFormattedData_PerfOS_Processor -ErrorAction Stop)
  $tot = $null
  $cores = New-Object Collections.Generic.List[object]
  foreach ($r in $rows) {
    if ($r.Name -eq '_Total') { $tot = $r; continue }
    $u = Clamp ([double]$r.PercentUserTime) 0 100
    $s = Clamp ([double]$r.PercentPrivilegedTime) 0 100
    $cores.Add([ordered]@{ u = (R1 $u); s = (R1 $s); w = 0.0 })
  }
  $total = 0.0; $user = 0.0; $sys = 0.0
  if ($tot) {
    $total = Clamp ([double]$tot.PercentProcessorTime) 0 100
    $user = Clamp ([double]$tot.PercentUserTime) 0 100
    $sys = Clamp ([double]$tot.PercentPrivilegedTime) 0 100
  }
  return [ordered]@{ total = (R1 $total); user = (R1 $user); system = (R1 $sys); iowait = 0.0; steal = 0.0; cores = $cores.ToArray() }
}

# Windows não tem "load average". Usamos o equivalente: núcleos ocupados + fila do processador,
# com média móvel exponencial de 1, 5 e 15 minutos (mesma escala do Linux: 1,0 = 1 núcleo cheio).
function Get-SystemCounters {
  $r = Get-CimInstance -ClassName Win32_PerfFormattedData_PerfOS_System -ErrorAction SilentlyContinue
  if ($r) { return @{ queue = [double]$r.ProcessorQueueLength } }
  return @{ queue = 0.0 }
}

function Update-Load([double]$cpuPct, [double]$queue, [int]$cores, [double]$interval) {
  $sample = ($cpuPct / 100.0 * $cores) + $queue
  if (-not $script:state.load) { $script:state.load = @($sample, $sample, $sample) }
  else {
    $periods = @(60.0, 300.0, 900.0)
    for ($i = 0; $i -lt 3; $i++) {
      $a = 1.0 - [math]::Exp(-$interval / $periods[$i])
      $script:state.load[$i] = $script:state.load[$i] + $a * ($sample - $script:state.load[$i])
    }
  }
  return @((R1 $script:state.load[0]), (R1 $script:state.load[1]), (R1 $script:state.load[2]))
}

function Get-MemData {
  $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
  $total = [long]$os.TotalVisibleMemorySize * 1024
  $avail = [long]$os.FreePhysicalMemory * 1024
  $used = [math]::Max(0L, $total - $avail)
  return [ordered]@{
    total = $total; used = $used; buffers = 0L; cache = 0L; free = $avail; available = $avail
    swap_total = [long]$script:state.swap.total; swap_used = [long]$script:state.swap.used
  }
}

function Get-DiskData {
  $out = New-Object Collections.Generic.List[object]
  foreach ($d in @(Get-CimInstance -ClassName Win32_LogicalDisk -Filter 'DriveType=3' -ErrorAction SilentlyContinue)) {
    if (-not $d.Size) { continue }
    $total = [long]$d.Size; $free = [long]$d.FreeSpace; $used = $total - $free
    $out.Add([ordered]@{
        mount = ([string]$d.DeviceID + '\'); dev = [string]$d.DeviceID; fs = [string]$d.FileSystem
        total = $total; used = $used; avail = $free; pct = (R1 (100.0 * $used / $total)); inode_pct = 0.0
      })
  }
  return $out.ToArray()
}

function Get-SwapData {
  $t = 0L; $u = 0L
  foreach ($p in @(Get-CimInstance -ClassName Win32_PageFileUsage -ErrorAction SilentlyContinue)) {
    $t += [long]$p.AllocatedBaseSize * 1MB; $u += [long]$p.CurrentUsage * 1MB
  }
  return @{ total = $t; used = $u }
}

function Get-DiskIO {
  $r = Get-CimInstance -ClassName Win32_PerfFormattedData_PerfDisk_PhysicalDisk -Filter "Name='_Total'" -ErrorAction SilentlyContinue
  if ($r) { return [ordered]@{ read_bps = [long]$r.DiskReadBytesPersec; write_bps = [long]$r.DiskWriteBytesPersec } }
  return [ordered]@{ read_bps = 0L; write_bps = 0L }
}

function Get-NetData([double]$elapsed) {
  $now = @{}
  $ifs = New-Object Collections.Generic.List[object]
  $trx = 0L; $ttx = 0L
  foreach ($n in [Net.NetworkInformation.NetworkInterface]::GetAllNetworkInterfaces()) {
    if ($n.OperationalStatus -ne 'Up' -or $n.NetworkInterfaceType -eq 'Loopback' -or $n.NetworkInterfaceType -eq 'Tunnel') { continue }
    try { $st = $n.GetIPv4Statistics() } catch { continue }
    $rx = [long]$st.BytesReceived; $tx = [long]$st.BytesSent
    $now[$n.Id] = @($rx, $tx)
    $rxb = 0L; $txb = 0L
    if ($script:state.prevNet.ContainsKey($n.Id) -and $elapsed -gt 0) {
      $p = $script:state.prevNet[$n.Id]
      $rxb = [long][math]::Max(0, ($rx - $p[0]) / $elapsed)
      $txb = [long][math]::Max(0, ($tx - $p[1]) / $elapsed)
    }
    $trx += $rxb; $ttx += $txb
    $ifs.Add([ordered]@{ name = [string]$n.Name; rx_bps = $rxb; tx_bps = $txb })
  }
  $script:state.prevNet = $now
  return [ordered]@{ rx_bps = $trx; tx_bps = $ttx; ifaces = $ifs.ToArray() }
}

# Processos: CPU = % de um núcleo (como top/htop), calculada pela variação do tempo de CPU.
function Get-ProcData([long]$totalMem, [double]$elapsed) {
  $newPrev = @{}
  $all = New-Object Collections.Generic.List[object]
  $threads = 0
  foreach ($p in [Diagnostics.Process]::GetProcesses()) {
    try {
      $id = $p.Id
      if ($id -eq 0) { continue }
      $sec = 0.0; try { $sec = $p.TotalProcessorTime.TotalSeconds } catch {}
      $rss = 0L; try { $rss = [long]$p.WorkingSet64 } catch {}
      $th = 0; try { $th = $p.Threads.Count } catch {}
      $threads += $th
      $newPrev[$id] = $sec
      $cpu = 0.0
      if ($elapsed -gt 0 -and $script:state.prevCpu.ContainsKey($id)) {
        $d = $sec - $script:state.prevCpu[$id]
        if ($d -gt 0) { $cpu = $d / $elapsed * 100.0 }
      }
      $all.Add([pscustomobject]@{ Id = $id; Name = [string]$p.ProcessName; Cpu = $cpu; Rss = $rss; Th = $th })
    } catch {} finally { $p.Dispose() }
  }
  $script:state.prevCpu = $newPrev

  $top = @(($all | Sort-Object Cpu -Descending | Select-Object -First 20) + ($all | Sort-Object Rss -Descending | Select-Object -First 20)) |
    Sort-Object Id -Unique | Sort-Object Cpu -Descending
  $procs = New-Object Collections.Generic.List[object]
  foreach ($p in $top) {
    $st = 'S'; if ($p.Cpu -ge 1) { $st = 'R' }
    $mem = 0.0; if ($totalMem -gt 0) { $mem = 100.0 * $p.Rss / $totalMem }
    $procs.Add([ordered]@{
        pid = $p.Id; user = ''; name = $p.Name; cmd = $p.Name; state = $st
        cpu = (R1 $p.Cpu); mem = (R1 $mem); rss = $p.Rss; threads = $p.Th
      })
  }
  $groups = @($all | Group-Object Name | ForEach-Object {
      [pscustomobject]@{
        Name = $_.Name; Rss = [long](($_.Group | Measure-Object Rss -Sum).Sum)
        Cpu = [double](($_.Group | Measure-Object Cpu -Sum).Sum); Count = $_.Count
      }
    } | Sort-Object Rss -Descending | Select-Object -First 15 | ForEach-Object {
      [ordered]@{ name = $_.Name; rss = $_.Rss; cpu = (R1 $_.Cpu); count = $_.Count }
    })
  $running = @($all | Where-Object { $_.Cpu -ge 1 }).Count
  $tasks = [ordered]@{ total = $all.Count; running = $running; sleeping = ($all.Count - $running); zombie = 0; threads = $threads }
  $names = @{}; foreach ($a in $all) { $names[[int]$a.Id] = $a.Name }
  return @{ procs = $procs.ToArray(); groups = $groups; tasks = $tasks; names = $names }
}

function Test-Loop([string]$a) { $a -eq '127.0.0.1' -or $a -eq '::1' -or $a.StartsWith('127.') }

# Portas escutando e conexões estabelecidas (coletor lento: a cada 30 s).
function Get-NetSockets($names) {
  $all = @(Get-NetTCPConnection -ErrorAction Stop)
  $listen = New-Object Collections.Generic.List[object]
  $seen = @{}
  $lports = @{}
  foreach ($c in $all) {
    if ("$($c.State)" -ne 'Listen') { continue }
    $addr = [string]$c.LocalAddress
    $port = [int]$c.LocalPort
    $lports[$port] = $true
    if ($addr -eq '0.0.0.0' -or $addr -eq '::') { $addr = '*' }
    $owner = [int]$c.OwningProcess
    $key = "$addr|$port|$owner"
    if ($seen.ContainsKey($key)) { continue }
    $seen[$key] = $true
    $proc = ''; if ($names.ContainsKey($owner)) { $proc = [string]$names[$owner] }
    $svc = Get-Svc $port $proc $true
    if ($owner -eq 4 -and $script:WebPorts -contains $port) { $proc = 'http.sys'; $svc = 'IIS/HTTP.sys' }
    $listen.Add([ordered]@{
        proto = 'tcp'; addr = $addr; port = $port; pid = $owner; proc = $proc; service = $svc
        exposed = [bool]($addr -eq '*' -or -not (Test-Loop $addr))
      })
  }
  $sorted = @($listen | Sort-Object { $_['port'] } | Select-Object -First 300)

  $agg = @{}
  foreach ($c in $all) {
    if ("$($c.State)" -ne 'Established') { continue }
    $owner = [int]$c.OwningProcess
    $proc = ''; if ($names.ContainsKey($owner)) { $proc = [string]$names[$owner] }
    $lp = [int]$c.LocalPort; $rp = [int]$c.RemotePort; $rip = [string]$c.RemoteAddress
    if ($lports.ContainsKey($lp)) { $dir = 'in'; $kp = $lp; $svc = Get-Svc $lp $proc $true }
    else { $dir = 'out'; $kp = $rp; $svc = Get-Svc $rp $proc $false }
    $k = "$dir|$proc|$rip|$kp"
    if ($agg.ContainsKey($k)) { $agg[$k]['count'] = $agg[$k]['count'] + 1; continue }
    $agg[$k] = [ordered]@{ dir = $dir; proc = $proc; remote = $rip; rport = $rp; lport = $lp; service = $svc; count = 1 }
  }
  $conns = @($agg.Values | Sort-Object { $_['count'] } -Descending | Select-Object -First 150)
  return @{ listen = $sorted; conns = $conns }
}

function Test-Http([string]$url, [int]$port, [string]$proc) {
  $r = [ordered]@{ url = $url; port = $port; proc = $proc; status = 0; ms = 0 }
  $sw = [Diagnostics.Stopwatch]::StartNew()
  try {
    $req = [Net.HttpWebRequest]::Create($url)
    $req.Method = 'GET'; $req.Timeout = 3000; $req.ReadWriteTimeout = 3000
    $req.AllowAutoRedirect = $false; $req.Proxy = $null; $req.UserAgent = "lynx-agent/$($script:Version)"
    if ($url.StartsWith('https')) { Enable-InsecureTls; $req.ServerCertificateValidationCallback = [LynxTls]::Any }
    $resp = $req.GetResponse(); $r['status'] = [int]$resp.StatusCode; $resp.Close()
  } catch [Net.WebException] {
    $resp = $_.Exception.Response
    if ($resp) { $r['status'] = [int]$resp.StatusCode; $resp.Close() }
    else {
      $sock = ''
      $ex = $_.Exception
      for ($i = 0; $ex -and $i -lt 4; $i++) {
        if ($ex -is [Net.Sockets.SocketException]) { $sock = "$($ex.SocketErrorCode)"; break }
        $ex = $ex.InnerException
      }
      $status = "$($_.Exception.Status)"
      if ($status -eq 'Timeout' -or $sock -eq 'TimedOut') { $r['err'] = 'timeout' }
      elseif ($status -eq 'ConnectFailure' -or $sock -eq 'ConnectionRefused') { $r['err'] = 'recusada' }
      else { $r['err'] = 'resposta inválida' }
    }
  } catch { $r['err'] = 'resposta inválida' }
  $r['ms'] = [int]$sw.ElapsedMilliseconds
  return $r
}

function Get-Probes($listen) {
  $out = New-Object Collections.Generic.List[object]
  $done = @{}
  foreach ($l in $listen) {
    if ($out.Count -ge 10) { break }
    if ($script:WebPorts -notcontains $l.port -or $done.ContainsKey($l.port)) { continue }
    $done[$l.port] = $true
    $hostAddr = '127.0.0.1'
    if ($l.addr -ne '*' -and -not (Test-Loop $l.addr) -and $l.addr -notmatch ':') { $hostAddr = $l.addr }
    $scheme = 'http'; if (@(443, 8443, 9443) -contains $l.port) { $scheme = 'https' }
    $out.Add((Test-Http "${scheme}://${hostAddr}:$($l.port)/" $l.port $l.proc))
  }
  return $out.ToArray()
}

# Eventos do Windows (System + Application): crítico, erro e aviso desde a última coleta.
function Get-EventData {
  $since = $script:state.logSince
  $script:state.logSince = Get-Date
  $ev = @(Get-WinEvent -FilterHashtable @{ LogName = @('System', 'Application'); Level = @(1, 2, 3); StartTime = $since } -MaxEvents 300 -ErrorAction SilentlyContinue)
  $ev = @($ev | Sort-Object TimeCreated)
  $dropped = 0
  if ($ev.Count -gt 60) { $dropped = $ev.Count - 60; $ev = @($ev | Select-Object -Last 60) }
  $out = New-Object Collections.Generic.List[object]
  foreach ($e in $ev) {
    $lvl = 'info'
    switch ([int]$e.Level) { 1 { $lvl = 'crit' } 2 { $lvl = 'error' } 3 { $lvl = 'warn' } }
    $m = [string]$e.Message
    if ($m) { $m = ($m -split "`r?`n")[0].Trim() }
    if (-not $m) { $m = 'Evento sem descrição' }
    $out.Add([ordered]@{
        t = (UnixMs $e.TimeCreated); src = [string]$e.ProviderName; level = $lvl; msg = (Clip "[$($e.Id)] $m" 400)
      })
  }
  return @{ logs = $out.ToArray(); dropped = $dropped }
}

function Get-Snapshot($cfg) {
  $st = $script:state
  $elapsed = $st.sw.Elapsed.TotalSeconds
  $st.sw.Restart()
  $first = ($null -eq $st.prevT)
  $st.prevT = Get-Date
  if ($first) { $elapsed = 0 }

  $now = Get-Date
  $slow = $first -or (($now - $st.lastSlow).TotalSeconds -ge 30)
  if ($slow) {
    $st.lastSlow = $now
    try { $st.disks = @(Get-DiskData) } catch { Write-Log "disco: $($_.Exception.Message)" }
    try { $st.swap = Get-SwapData } catch {}
  }

  $cpu = Get-CpuData
  $sys = Get-SystemCounters
  $cores = [int][Environment]::ProcessorCount
  $load = Update-Load $cpu.total $sys.queue $cores $cfg.interval
  $mem = Get-MemData
  $pd = Get-ProcData ([long]$mem.total) $elapsed
  $net = Get-NetData $elapsed

  $snap = [ordered]@{
    name = $cfg.name; version = $script:Version; interval = $cfg.interval; ts = [long]([datetimeoffset]::UtcNow.ToUnixTimeSeconds())
    slow = [bool]$slow; host = (Get-HostInfo); cpu = $cpu; load = $load; mem = $mem; disks = @($st.disks)
    diskio = (Get-DiskIO); net = $net; tasks = $pd.tasks; procs = $pd.procs; groups = $pd.groups
  }

  if ($slow) {
    try {
      $s = Get-NetSockets $pd.names
      $st.listen = $s.listen; $st.conns = $s.conns
      $st.probes = @(Get-Probes $s.listen)
    } catch { Write-Log "sockets: $($_.Exception.Message)" }
    try {
      $e = Get-EventData
      $st.pendingLogs = @($st.pendingLogs) + @($e.logs)
      $st.dropped += $e.dropped
    } catch { Write-Log "eventos: $($_.Exception.Message)" }
    $snap['listen'] = @($st.listen); $snap['conns'] = @($st.conns); $snap['probes'] = @($st.probes)
  }
  if (@($st.pendingLogs).Count -gt 0) {
    $snap['logs'] = @($st.pendingLogs)
    if ($st.dropped -gt 0) { $snap['logs_dropped'] = $st.dropped }
  }
  return $snap
}

# ----------------------------------------------------------------------------------------------
# envio
# ----------------------------------------------------------------------------------------------
function Send-Snapshot($cfg, $snap) {
  $json = ConvertTo-Json -InputObject $snap -Depth 8 -Compress
  $raw = [Text.Encoding]::UTF8.GetBytes($json)
  $ms = New-Object IO.MemoryStream
  $gz = New-Object IO.Compression.GZipStream($ms, [IO.Compression.CompressionMode]::Compress)
  $gz.Write($raw, 0, $raw.Length); $gz.Close()
  $body = $ms.ToArray()
  try {
    [Net.ServicePointManager]::Expect100Continue = $false
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch {}
    $req = [Net.HttpWebRequest]::Create($cfg.server + '/api/ingest')
    $req.Method = 'POST'; $req.ContentType = 'application/json'; $req.Timeout = 10000; $req.ReadWriteTimeout = 10000
    $req.Proxy = $null; $req.UserAgent = "lynx-agent/$($script:Version)"
    $req.Headers.Add('Content-Encoding', 'gzip')
    $req.Headers.Add('Authorization', 'Bearer ' + $cfg.token)
    if ($cfg.insecure -and $cfg.server.StartsWith('https')) { Enable-InsecureTls; $req.ServerCertificateValidationCallback = [LynxTls]::Any }
    $req.ContentLength = $body.Length
    $rs = $req.GetRequestStream(); $rs.Write($body, 0, $body.Length); $rs.Close()
    $resp = $req.GetResponse(); $resp.Close()
    return @{ ok = $true; code = 204; msg = '' }
  } catch [Net.WebException] {
    $code = 0; if ($_.Exception.Response) { $code = [int]$_.Exception.Response.StatusCode }
    return @{ ok = $false; code = $code; msg = $_.Exception.Message }
  } catch {
    return @{ ok = $false; code = 0; msg = $_.Exception.Message }
  }
}

function Invoke-Cycle($cfg) {
  $snap = Get-Snapshot $cfg
  if ($Dump) { ConvertTo-Json -InputObject $snap -Depth 8 | Out-Host; return @{ ok = $true; code = 0; msg = '' } }
  $r = Send-Snapshot $cfg $snap
  if ($r.ok) { $script:state.pendingLogs = @(); $script:state.dropped = 0 }
  elseif (@($script:state.pendingLogs).Count -gt 200) { $script:state.pendingLogs = @($script:state.pendingLogs | Select-Object -Last 200) }
  return $r
}

# ----------------------------------------------------------------------------------------------
# instalação / remoção
# ----------------------------------------------------------------------------------------------
function Test-Admin {
  ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Stop-Agent {
  try { Stop-ScheduledTask -TaskName $script:TaskName -ErrorAction SilentlyContinue } catch {}
  try {
    Get-CimInstance -ClassName Win32_Process -Filter "Name='powershell.exe'" -ErrorAction SilentlyContinue |
      Where-Object { $_.CommandLine -like '*lynx-agent.ps1*' -and $_.ProcessId -ne $PID } |
      ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
  } catch {}
}

function Install-Agent($cfg) {
  if (-not (Test-Admin)) { throw 'Execute o PowerShell como Administrador.' }
  if (-not $cfg.server) {
    $s = Read-Host 'Endereço do servidor Lynx (ex.: http://192.168.1.10:8080)'
    $cfg.server = ([string]$s).Trim().TrimEnd('/')
    if ($cfg.server -and $cfg.server -notmatch '^https?://') { $cfg.server = 'http://' + $cfg.server }
  }
  if (-not $cfg.token) {
    $sec = Read-Host 'Token do servidor Lynx (variável TOKEN do .env)' -AsSecureString
    $cfg.token = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
  }
  if (-not $cfg.server -or -not $cfg.token) { throw 'Servidor e token são obrigatórios.' }

  Write-Host "→ Testando conexão com $($cfg.server)"
  try {
    $h = [Net.HttpWebRequest]::Create($cfg.server + '/healthz'); $h.Timeout = 5000; $h.Proxy = $null
    if ($cfg.insecure -and $cfg.server.StartsWith('https')) { Enable-InsecureTls; $h.ServerCertificateValidationCallback = [LynxTls]::Any }
    $h.GetResponse().Close()
  } catch { throw "Não consegui falar com $($cfg.server): $($_.Exception.Message). Confira IP, porta e firewall." }

  Write-Host '→ Validando token (enviando uma amostra)'
  $r = Invoke-Cycle $cfg
  if (-not $r.ok) {
    if ($r.code -eq 401) { throw 'Token inválido: use o mesmo valor de TOKEN do servidor Lynx.' }
    throw "Falha ao enviar dados ($($r.code)): $($r.msg)"
  }

  Stop-Agent
  $app = Get-AppDir; $data = Get-DataDir
  foreach ($d in $app, $data) { if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null } }
  $dest = Join-Path $app 'lynx-agent.ps1'
  if ((Resolve-Path $PSCommandPath).Path -ne $dest) { Copy-Item -LiteralPath $PSCommandPath -Destination $dest -Force }

  $conf = [ordered]@{ server = $cfg.server; token = $cfg.token; name = $cfg.name; interval = $cfg.interval; insecure = [bool]$cfg.insecure }
  [IO.File]::WriteAllText((Join-Path $data 'agent.json'), ($conf | ConvertTo-Json), (New-Object Text.UTF8Encoding($false)))
  # o token é segredo: só SYSTEM e Administradores leem
  try { & icacls.exe $data /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null } catch {}

  $arg = "-NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$dest`""
  $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument $arg
  $trigger = New-ScheduledTaskTrigger -AtStartup
  $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
  $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew
  Register-ScheduledTask -TaskName $script:TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings `
    -Description 'Agente de monitoramento Lynx' -Force | Out-Null
  Start-ScheduledTask -TaskName $script:TaskName
  Start-Sleep -Seconds 3
  $state = (Get-ScheduledTask -TaskName $script:TaskName).State

  Write-Host ''
  Write-Host "✓ Agente Lynx instalado (tarefa '$($script:TaskName)': $state)" -ForegroundColor Green
  Write-Host "  Servidor : $($cfg.server)"
  Write-Host "  Nome     : $($cfg.name)  (aparece no painel em alguns segundos)"
  Write-Host "  Log      : $(Join-Path $data 'agent.log')"
  Write-Host '  Remover  : .\lynx-agent.ps1 -Uninstall'
}

function Uninstall-Agent {
  if (-not (Test-Admin)) { throw 'Execute o PowerShell como Administrador.' }
  Stop-Agent
  try { Unregister-ScheduledTask -TaskName $script:TaskName -Confirm:$false -ErrorAction SilentlyContinue } catch {}
  foreach ($d in (Get-AppDir), (Get-DataDir)) { if (Test-Path $d) { Remove-Item -Recurse -Force $d -ErrorAction SilentlyContinue } }
  Write-Host '✓ Agente Lynx removido.' -ForegroundColor Green
}

# ----------------------------------------------------------------------------------------------
# principal
# ----------------------------------------------------------------------------------------------
function Invoke-Main {
  $cfg = Get-Config $Server $Token $Name $Interval $Insecure.IsPresent

  if ($Uninstall) { Uninstall-Agent; return }
  if ($Install) { Install-Agent $cfg; return }

  if (-not $Dump -and (-not $cfg.server -or -not $cfg.token)) {
    Write-Host 'Servidor/token não configurados. Use -Server e -Token, ou rode com -Install.'
    return
  }
  try { [Diagnostics.Process]::GetCurrentProcess().PriorityClass = 'BelowNormal' } catch {}
  Write-Log "agente $($script:Version) iniciado: servidor=$($cfg.server) nome=$($cfg.name) intervalo=$($cfg.interval)s"

  $lastErr = [datetime]::MinValue
  $wasDown = $false
  while ($true) {
    $t0 = Get-Date
    try {
      $r = Invoke-Cycle $cfg
      if ($r.ok) {
        if ($wasDown) { Write-Log 'conexão com o servidor restabelecida'; $wasDown = $false }
      } else {
        $wasDown = $true
        if (((Get-Date) - $lastErr).TotalSeconds -gt 60) { Write-Log "envio falhou ($($r.code)): $($r.msg)"; $lastErr = Get-Date }
        if ($Once) { Write-Host "Falha ao enviar ($($r.code)): $($r.msg)"; exit 1 }
      }
    } catch {
      if (((Get-Date) - $lastErr).TotalSeconds -gt 60) { Write-Log "erro no ciclo: $($_.Exception.Message)"; $lastErr = Get-Date }
      if ($Once) { throw }
    }
    if ($Once) { return }
    $script:state.cycles++
    if ($script:state.cycles % 120 -eq 0) { [GC]::Collect() }
    $wait = $cfg.interval - ((Get-Date) - $t0).TotalSeconds
    if ($wait -lt 1) { $wait = 1 }
    Start-Sleep -Milliseconds ([int]($wait * 1000))
  }
}

# Quando o arquivo é "dot-sourced" (testes), só carrega as funções.
if ($MyInvocation.InvocationName -ne '.') { Invoke-Main }
