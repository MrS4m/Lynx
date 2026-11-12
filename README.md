# Lynx

Monitoramento de servidores Linux com **agente mínimo** e painel web. Cada servidor roda um agente de ~6 MB que lê `/proc` e envia métricas, portas, conexões e erros de log; o painel mostra tudo como um htop informativo, com gráficos, alertas e um **modo TV** para telão.

```
 ┌──────────────┐   HTTP POST (JSON + gzip, a cada 10 s)   ┌────────────────────────────┐
 │ servidor A   │ ───────────────────────────────────────▶ │  Lynx (container Docker)│
 │ lynx-agent│                                          │  • recebe e guarda dados   │
 ├──────────────┤                                          │  • avalia alertas          │
 │ servidor B   │ ───────────────────────────────────────▶ │  • serve o painel web      │
 │ lynx-agent│                                          └─────────────┬──────────────┘
 └──────────────┘                                                        │ http://monitor.lan
                                                                   navegador / TV
```

O agente só faz conexões **de saída** para o painel. Nenhuma porta precisa ser aberta nos servidores monitorados.

## O que aparece no painel

| Área | Conteúdo |
|---|---|
| Visão geral | Situação de cada servidor, CPU/memória/disco, serviços detectados, gráficos de colunas, rosca e barras, alertas ativos |
| Servidor | Medidores por núcleo estilo htop, tabela de processos (por CPU ou memória), load average, swap, rede, I/O de disco, histórico de 1 h e 24 h |
| Quem consome o quê | Memória por aplicação, uso dos discos, conexões por serviço |
| Servidores web | Teste HTTP local em cada porta web (80, 443, 8080…) com código e latência |
| Portas e conexões | Portas abertas (e se estão expostas à rede), e com quem a máquina conversa: `java conecta em 10.0.2.15:5432 PostgreSQL`, `zabbix_agentd conecta em 10.0.1.5:10051 Zabbix Server` |
| Logs | Erros e avisos do journald e de logs de Nginx, Apache, PostgreSQL, MySQL, Zabbix, Wazuh, Tomcat |
| Modo TV | Resumo geral em tela cheia, com rotação de páginas quando há muitos servidores |

## Alertas

Avaliados a cada envio, com histerese para não piscar. Estes são os limites padrão:

| Regra | Atenção | Crítico |
|---|---|---|
| CPU (média das últimas 3 amostras) | 75% | 90% |
| Memória (usa `MemAvailable`, ignora cache) | 80% | 92% |
| Swap | 40% | 75% |
| Load average de 5 min por núcleo | 1,5 | 3,0 |
| Disco / inodes | 80% / 85% | 92% / 95% |
| **Previsão de disco cheio** (regressão linear, precisa de 1 h de dados) | enche em menos de 48 h | menos de 6 h |
| **Tendência de memória** (possível vazamento) | chega a 95% em menos de 3 h | — |
| Teste web | timeout de 2 s | HTTP 5xx ou conexão recusada |
| Serviço parou | — | porta conhecida deixou de escutar |
| Logs | 20 erros em 5 min | 100 erros em 5 min, ou linha crítica (OOM, panic, segfault, erro de I/O) |
| Agente sem resposta | — | sem dados por 3× o intervalo (mínimo 30 s) |
| Nova porta exposta | evento informativo | — |

Com `WEBHOOK_URL` definido, alertas novos, escalonados e recuperados críticos são enviados a Slack, Discord ou Mattermost.

## Passo a passo de implantação

### 1. Subir o painel (uma vez, em um servidor com Docker)

```bash
git clone https://github.com/SEU_USUARIO/lynx.git && cd lynx   # ou extraia o zip e entre na pasta
cp .env.example .env
nano .env        # defina TOKEN (openssl rand -hex 24), DASH_USER e DASH_PASS
docker compose up -d --build
docker compose ps                  # deve mostrar "healthy" em ~30 s
```

Abra `http://IP-DO-SERVIDOR:8080`. Para acessar sem `:8080`, defina `PORT=80` no `.env`.

### 2. Dar um nome DNS (acesso local)

Escolha uma das opções, apontando `monitor.lan` para o IP do servidor do painel:

- **DNS da rede** (Pi-hole, dnsmasq, BIND, Active Directory, roteador): crie um registro `A` → `monitor.lan = 192.168.1.50`. É a melhor opção, pois vale para todos os computadores e para os agentes.
- **Só no seu computador**: adicione `192.168.1.50 monitor.lan` em `/etc/hosts` (Linux/macOS) ou `C:\Windows\System32\drivers\etc\hosts` (Windows).

Depois acesse `http://monitor.lan:8080` (ou `http://monitor.lan` com `PORT=80`).

HTTPS com Nginx na frente (recomendado se os agentes ou usuários cruzam redes não confiáveis):

```nginx
server {
    listen 443 ssl;
    server_name monitor.lan;
    ssl_certificate     /etc/ssl/monitor.crt;
    ssl_certificate_key /etc/ssl/monitor.key;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

### 3. Instalar o agente em cada servidor

No servidor a monitorar, como administrador (use o nome DNS que você criou):

```bash
curl -fsSL http://monitor.lan:8080/install.sh | sudo TOKEN=SEU_TOKEN bash
```

O script baixa o binário certo (amd64 ou arm64) direto do painel, cria `/etc/lynx/agent.env` (permissão 600) e o serviço `lynx-agent` no systemd, com limites de 5% de CPU e 64 MB de memória. O servidor aparece no painel em segundos.

Opções: `NAME=web-01` (nome exibido), `INTERVAL=15` (segundos), `LOGS="/var/log/app/*.log"` (logs extras).

```bash
systemctl status lynx-agent               # conferir
journalctl -u lynx-agent -n 30 --no-pager # se algo falhar
curl -fsSL http://monitor.lan:8080/install.sh | sudo bash -s -- uninstall   # remover
```

Sem systemd, baixe `http://monitor.lan:8080/download/lynx-agent-linux-amd64` e rode com `AGENT_SERVER=http://monitor.lan:8080 AGENT_TOKEN=... ./lynx-agent`. Para ver o que ele coleta sem enviar nada: `./lynx-agent -once`.

#### Servidores Windows

O agente Windows é um único script PowerShell (`agent.ps1`), embutido no próprio servidor Lynx. No servidor Windows, abra o PowerShell **como Administrador** e rode:

```powershell
$env:LYNX_TOKEN='SEU_TOKEN'; irm http://monitor.lan:8080/install.ps1 | iex
```

O endereço do Lynx já vai embutido no comando (é o que você usou para abrir o instalador). Ele baixa o agente, testa a conexão e o token, grava `C:\ProgramData\Lynx\agent.json` (só SYSTEM e Administradores leem) e registra a tarefa agendada `LynxAgent`, que roda como SYSTEM na inicialização e reinicia sozinha se cair. Opções: `$env:LYNX_NAME='web-01'`, `$env:LYNX_INTERVAL='30'`.

Instalação manual, informando o IP do Lynx na hora (sem a variável de ambiente, ele pergunta endereço e token):

```powershell
# baixe http://monitor.lan:8080/download/lynx-agent.ps1 e, como Administrador:
.\lynx-agent.ps1 -Install -Server http://IP_DO_LYNX:8080 -Token SEU_TOKEN
.\lynx-agent.ps1 -Uninstall                                   # remover
.\lynx-agent.ps1 -Server http://IP_DO_LYNX:8080 -Token SEU_TOKEN -Once -Dump   # ver o JSON sem enviar
```

Log do agente: `C:\ProgramData\Lynx\agent.log`. Requisitos: Windows Server 2012 R2 / Windows 8.1 ou superior (PowerShell 5.1 recomendado). Só faz conexões de saída; não abre portas.

Diferenças em relação ao agente Linux: é um script, então consome mais que o binário Go (roda com prioridade baixa e intervalo padrão de 15 s); "load average" é calculado como núcleos ocupados + fila do processador (mesma escala); o campo *usuário* dos processos fica vazio; os logs vêm do Visualizador de Eventos (System e Application: crítico, erro e aviso); processos que usam a porta 80/443 pelo IIS aparecem como `http.sys`.

### 4. Modo TV

Clique em **Modo TV** no topo (ou abra `http://monitor.lan:8080/#/tv`). Tecla `F` alterna tela cheia, `Esc` sai.

Para uma TV de parede que não consegue fazer login, defina `TV_KEY` no `.env` e abra `http://monitor.lan:8080/?key=SUA_CHAVE#/tv`. Essa chave libera **somente** o resumo geral e a lista de alertas: não dá acesso a logs, processos, detalhe dos servidores nem a remover nada.

### 5. Manutenção

```bash
docker compose logs -f lynx                 # logs do painel
git pull && docker compose up -d --build       # atualizar
docker run --rm -v lynx_lynx-data:/d -v "$PWD":/b alpine tar czf /b/lynx-backup.tgz -C /d .   # backup do histórico
```

Remover um servidor desativado: abra o servidor no painel e clique em **Remover**.

## Variáveis de configuração

**Painel (`.env`)**: `TOKEN` (obrigatório), `PORT`, `DASH_USER`/`DASH_PASS`, `TV_KEY`, `WEBHOOK_URL`, `TZ`.

**Agente** (`/etc/lynx/agent.env`):

| Variável | Padrão | Função |
|---|---|---|
| `AGENT_SERVER` / `AGENT_TOKEN` | — | Endereço do painel e token |
| `AGENT_NAME` | hostname | Nome exibido |
| `AGENT_INTERVAL` | `10` | Segundos entre envios (mínimo 2) |
| `AGENT_LOGS` | — | Arquivos de log extras (globs, separados por vírgula) |
| `AGENT_LOG_ALL` | `0` | `1` envia todas as linhas, não só erros e avisos (gera bem mais tráfego) |
| `AGENT_PROBES` | — | URLs extras para teste HTTP, separadas por vírgula |
| `AGENT_HIDE_CMDLINE` | `0` | `1` envia só o nome do processo, sem argumentos (evita expor senhas passadas por parâmetro) |

## Consumo do agente

Medido com o agente real durante 60 s em uma VM de 2 núcleos com 66 processos, enviando a cada 10 s: **9,6 MB de memória residente, 0,03% de um núcleo, 5 threads**, binário de 6,2 MB (5,8 MB em arm64). Em máquinas com milhares de processos a leitura de `/proc` custa mais; aumente `AGENT_INTERVAL` se necessário.

## Segurança

- Defina sempre `DASH_USER`/`DASH_PASS`. Sem eles o painel abre sem senha.
- O token é um segredo compartilhado entre os agentes. Em redes não confiáveis, publique o painel atrás de HTTPS (exemplo de Nginx acima).
- O agente roda como root (precisa ler `/proc/*/fd` e logs de outros usuários), mas o serviço systemd é restrito: sistema de arquivos somente leitura, sem novos privilégios, limites de CPU e memória.
- O painel mostra a linha de comando dos processos e mensagens de log. Se houver segredos em argumentos ou logs, use `AGENT_HIDE_CMDLINE=1` e restrinja quem acessa o painel.
- O contêiner roda sem root, com sistema de arquivos somente leitura e sem capabilities.

## Demonstração sem servidores reais

O simulador gera 7 servidores fictícios (Nginx, PostgreSQL, Zabbix, Wazuh, Java com erro 503, proxy saudável e um agente que cai), com 90 min de histórico. Serve para testar a interface e fazer prints sem expor máquinas reais:

```bash
docker compose up -d --build
go run ./cmd/simulador -server http://127.0.0.1:8080 -token SEU_TOKEN
```

## Limites do protótipo

- Histórico guardado em memória, com cópia em disco a cada 60 s e ao encerrar: 1 hora com amostras brutas e 24 horas com médias por minuto. Uma queda abrupta perde até 1 minuto. Suficiente para dezenas de servidores; para centenas ou retenção longa, o próximo passo é trocar por PostgreSQL/Timescale ou VictoriaMetrics.
- Um único usuário de painel e sem HTTPS embutido (use proxy reverso).
- Agente apenas para Linux. Contêineres Docker aparecem como processos do host, ainda sem métricas por contêiner.
- Sem reconhecimento (ack) nem silenciamento de alertas, exceto "Dispensar" para serviço parado.

## Próximos passos sugeridos

1. Métricas por contêiner (Docker/Podman) e por serviço systemd.
2. Armazenamento em banco de séries temporais e retenção de 30+ dias.
3. Usuários e papéis, reconhecimento e silenciamento de alertas, agendamento de manutenção.
4. Notificações por e-mail e Telegram; limites de alerta configuráveis por servidor.
5. Agente para Windows.

## Trocar o logotipo

O logotipo do Lynx vem de **dois arquivos** em `cmd/server/web/`:

- `logo.png`: topo do painel e modo TV (256×256, fundo transparente).
- `favicon.png`: aba do navegador (64×64).

Para trocar, substitua os dois mantendo os nomes e rode `docker compose up -d --build`. Imagem quadrada com cantos transparentes funciona melhor. Se usar SVG, salve como `logo.svg`, troque `logo.png` por `logo.svg` em `index.html` e `app.js` e libere `/logo.svg` na lista pública de `guard()` em `cmd/server/main.go`.

## Estrutura do projeto

```
cmd/agent       agente (lê /proc, portas, conexões, logs)
cmd/server      painel: API, alertas, persistência, web/ (HTML, CSS, JS, Chart.js, fontes)
cmd/simulador   gerador de servidores fictícios
internal/model  contrato JSON entre agente e servidor
Dockerfile, docker-compose.yml, .env.example
```

Sem dependências externas em Go (só biblioteca padrão). O painel funciona offline: Chart.js e as fontes IBM Plex vão embutidos no binário.

## Status de teste

Testado no ambiente de desenvolvimento: coleta do agente em um Linux real, envio e recebimento, motor de alertas (CPU, memória, disco, previsão de enchimento, 5xx, serviço parado, logs críticos, agente offline), persistência após reinício, autenticação (login, chave de TV, token de ingestão, bloqueio de path traversal), sintaxe do instalador e a interface completa em navegador. **Não foi possível testar** neste ambiente: o `docker compose up` (sem daemon Docker disponível), a instalação via systemd (`install.sh`) e o **agente Windows em uma máquina Windows real**. O script PowerShell foi validado quanto à sintaxe e, no PowerShell 7 em Linux com as chamadas exclusivas do Windows simuladas, enviou dados que o servidor aceitou e exibiu corretamente (token errado → 401). Rode tudo primeiro em uma VM de teste.
