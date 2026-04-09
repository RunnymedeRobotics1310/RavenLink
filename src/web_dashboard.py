"""Local web dashboard for bridge status monitoring and config editing."""

import json
import logging
import threading
from typing import Optional

from flask import Flask, jsonify, request, Response

from .bridge_status import BridgeStatus
from .config import Config

log = logging.getLogger(__name__)

DASHBOARD_HTML = """<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>FRC OBS Bridge</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; }
  .header { background: #1e293b; padding: 16px 24px; border-bottom: 1px solid #334155; display: flex; align-items: center; gap: 12px; }
  .header h1 { font-size: 18px; font-weight: 600; }
  .dot { width: 12px; height: 12px; border-radius: 50%; display: inline-block; }
  .dot.green { background: #22c55e; box-shadow: 0 0 8px #22c55e; }
  .dot.yellow { background: #eab308; box-shadow: 0 0 8px #eab308; }
  .dot.red { background: #ef4444; box-shadow: 0 0 8px #ef4444; }
  .dot.gray { background: #6b7280; }
  .container { max-width: 1200px; margin: 0 auto; padding: 24px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; margin-bottom: 24px; }
  .card { background: #1e293b; border-radius: 8px; padding: 16px; border: 1px solid #334155; }
  .card h2 { font-size: 14px; color: #94a3b8; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 12px; }
  .stat { display: flex; justify-content: space-between; padding: 6px 0; border-bottom: 1px solid #334155; }
  .stat:last-child { border-bottom: none; }
  .stat .label { color: #94a3b8; }
  .stat .value { font-weight: 500; }
  .stat .value.ok { color: #22c55e; }
  .stat .value.warn { color: #eab308; }
  .stat .value.err { color: #ef4444; }
  .tabs { display: flex; gap: 0; margin-bottom: 16px; border-bottom: 1px solid #334155; }
  .tab { padding: 10px 20px; cursor: pointer; color: #94a3b8; border-bottom: 2px solid transparent; }
  .tab.active { color: #e2e8f0; border-bottom-color: #3b82f6; }
  .tab:hover { color: #e2e8f0; }
  .tab-content { display: none; }
  .tab-content.active { display: block; }
  .log-box { background: #0f172a; border: 1px solid #334155; border-radius: 4px; padding: 12px; font-family: monospace; font-size: 12px; max-height: 400px; overflow-y: auto; line-height: 1.6; white-space: pre-wrap; word-break: break-all; }
  .config-section { margin-bottom: 24px; }
  .config-section h3 { font-size: 14px; color: #3b82f6; margin-bottom: 12px; padding-bottom: 8px; border-bottom: 1px solid #334155; }
  .field { display: grid; grid-template-columns: 200px 1fr; gap: 12px; align-items: center; padding: 8px 0; }
  .field label { color: #94a3b8; font-size: 13px; }
  .field input, .field select { background: #0f172a; border: 1px solid #334155; border-radius: 4px; padding: 8px 12px; color: #e2e8f0; font-size: 14px; }
  .field input:focus, .field select:focus { outline: none; border-color: #3b82f6; }
  .field .restart-badge { font-size: 10px; color: #eab308; margin-left: 4px; }
  .btn { padding: 10px 20px; border-radius: 6px; border: none; cursor: pointer; font-size: 14px; font-weight: 500; }
  .btn-primary { background: #3b82f6; color: white; }
  .btn-primary:hover { background: #2563eb; }
  .btn-row { display: flex; gap: 8px; margin-top: 16px; }
  .banner { padding: 10px 16px; border-radius: 6px; margin-bottom: 16px; font-size: 13px; }
  .banner.warn { background: #422006; border: 1px solid #854d0e; color: #fbbf24; }
  .banner.success { background: #052e16; border: 1px solid #166534; color: #4ade80; }
  .hidden { display: none; }
</style>
</head>
<body>
<div class="header">
  <span class="dot gray" id="status-dot"></span>
  <h1>FRC OBS Bridge</h1>
  <span id="header-status" style="color:#94a3b8; margin-left:auto; font-size:13px;"></span>
</div>
<div class="container">
  <div class="tabs">
    <div class="tab active" data-tab="status">Status</div>
    <div class="tab" data-tab="logs">Logs</div>
    <div class="tab" data-tab="config">Config</div>
  </div>

  <div class="tab-content active" id="tab-status">
    <div class="grid">
      <div class="card">
        <h2>Connections</h2>
        <div class="stat"><span class="label">NetworkTables</span><span class="value" id="s-nt">—</span></div>
        <div class="stat"><span class="label">OBS Studio</span><span class="value" id="s-obs">—</span></div>
        <div class="stat"><span class="label">RavenBrain</span><span class="value" id="s-rb">—</span></div>
      </div>
      <div class="card">
        <h2>Match State</h2>
        <div class="stat"><span class="label">State</span><span class="value" id="s-state">—</span></div>
        <div class="stat"><span class="label">OBS Recording</span><span class="value" id="s-rec">—</span></div>
      </div>
      <div class="card">
        <h2>Telemetry</h2>
        <div class="stat"><span class="label">Session File</span><span class="value" id="s-file">—</span></div>
        <div class="stat"><span class="label">Entries Written</span><span class="value" id="s-entries">—</span></div>
        <div class="stat"><span class="label">Rate</span><span class="value" id="s-rate">—</span></div>
        <div class="stat"><span class="label">Topics</span><span class="value" id="s-topics">—</span></div>
      </div>
      <div class="card">
        <h2>Upload</h2>
        <div class="stat"><span class="label">Pending</span><span class="value" id="s-pending">—</span></div>
        <div class="stat"><span class="label">Uploaded</span><span class="value" id="s-uploaded">—</span></div>
        <div class="stat"><span class="label">Status</span><span class="value" id="s-ulstatus">—</span></div>
      </div>
    </div>
  </div>

  <div class="tab-content" id="tab-logs">
    <div class="log-box" id="log-output"></div>
  </div>

  <div class="tab-content" id="tab-config">
    <div id="config-banner" class="banner warn hidden"></div>
    <div id="config-save-banner" class="banner success hidden"></div>
    <form id="config-form"></form>
    <div class="btn-row">
      <button class="btn btn-primary" id="btn-save">Save</button>
    </div>
  </div>
</div>
<script>
const RESTART_FIELDS = new Set(['team','obs_host','obs_port','obs_password','dashboard_port','dashboard_enabled']);
const FIELD_DESCS = {
  team:'Team number',obs_host:'OBS host',obs_port:'OBS port',obs_password:'OBS password',
  stop_delay:'Stop delay (s)',poll_interval:'Poll interval (s)',log_level:'Log level',
  auto_teleop_gap:'Auto-teleop gap (s)',nt_disconnect_grace:'NT disconnect grace (s)',
  launch_on_login:'Launch on login',nt_paths:'NT path prefixes',data_dir:'Data directory',
  retention_days:'Retention (days)',ravenbrain_url:'RavenBrain URL',ravenbrain_api_key:'API key',
  ravenbrain_batch_size:'Batch size',ravenbrain_upload_interval:'Upload interval (s)',
  dashboard_enabled:'Dashboard enabled',dashboard_port:'Dashboard port'
};
const SENSITIVE = new Set(['obs_password','ravenbrain_api_key']);
const SECTIONS = {bridge:['team','obs_host','obs_port','obs_password','stop_delay','poll_interval','log_level','auto_teleop_gap','nt_disconnect_grace','launch_on_login'],
  telemetry:['nt_paths','data_dir','retention_days'],
  ravenbrain:['ravenbrain_url','ravenbrain_api_key','ravenbrain_batch_size','ravenbrain_upload_interval'],
  dashboard:['dashboard_enabled','dashboard_port']};

// Tabs
document.querySelectorAll('.tab').forEach(t=>{
  t.addEventListener('click',()=>{
    document.querySelectorAll('.tab').forEach(x=>x.classList.remove('active'));
    document.querySelectorAll('.tab-content').forEach(x=>x.classList.remove('active'));
    t.classList.add('active');
    document.getElementById('tab-'+t.dataset.tab).classList.add('active');
    if(t.dataset.tab==='config') loadConfig();
  });
});

function connClass(v){return v?'ok':'err';}
function connText(v){return v?'Connected':'Disconnected';}

function updateStatus(d){
  const dot=document.getElementById('status-dot');
  dot.className='dot '+(d.nt_connected&&d.obs_connected?'green':d.nt_connected||d.obs_connected?'yellow':'red');
  document.getElementById('header-status').textContent=d.match_state+(d.obs_recording?' | REC':'');
  document.getElementById('s-nt').className='value '+connClass(d.nt_connected);
  document.getElementById('s-nt').textContent=connText(d.nt_connected);
  document.getElementById('s-obs').className='value '+connClass(d.obs_connected);
  document.getElementById('s-obs').textContent=connText(d.obs_connected);
  document.getElementById('s-rb').className='value '+connClass(d.ravenbrain_reachable);
  document.getElementById('s-rb').textContent=connText(d.ravenbrain_reachable);
  document.getElementById('s-state').textContent=d.match_state;
  document.getElementById('s-rec').className='value '+(d.obs_recording?'ok':'');
  document.getElementById('s-rec').textContent=d.obs_recording?'Yes':'No';
  document.getElementById('s-file').textContent=d.active_session_file||'None';
  document.getElementById('s-entries').textContent=d.entries_written.toLocaleString();
  document.getElementById('s-rate').textContent=d.entries_per_second.toFixed(1)+'/s';
  document.getElementById('s-topics').textContent=d.subscribed_topics;
  document.getElementById('s-pending').textContent=d.files_pending;
  document.getElementById('s-uploaded').textContent=d.files_uploaded;
  document.getElementById('s-ulstatus').textContent=d.currently_uploading?'Uploading...':(d.last_upload_result||'Idle');
  document.getElementById('log-output').textContent=(d.recent_logs||[]).join('\\n');
  const lb=document.getElementById('log-output');
  lb.scrollTop=lb.scrollHeight;
}

function loadConfig(){
  fetch('/api/config').then(r=>r.json()).then(cfg=>{
    const form=document.getElementById('config-form');
    form.innerHTML='';
    for(const[section,fields]of Object.entries(SECTIONS)){
      const div=document.createElement('div');
      div.className='config-section';
      div.innerHTML='<h3>'+section+'</h3>';
      for(const f of fields){
        const val=cfg[f];
        const fd=document.createElement('div');
        fd.className='field';
        const lb=document.createElement('label');
        lb.textContent=(FIELD_DESCS[f]||f)+(RESTART_FIELDS.has(f)?' ⟳':'');
        if(RESTART_FIELDS.has(f)){const sp=document.createElement('span');sp.className='restart-badge';sp.textContent='restart required';lb.appendChild(sp);}
        fd.appendChild(lb);
        if(typeof val==='boolean'){
          const sel=document.createElement('select');
          sel.name=f;
          sel.innerHTML='<option value="true"'+(val?' selected':'')+'>true</option><option value="false"'+(!val?' selected':'')+'>false</option>';
          fd.appendChild(sel);
        }else if(f==='log_level'){
          const sel=document.createElement('select');
          sel.name=f;
          ['DEBUG','INFO','WARNING','ERROR'].forEach(lv=>{sel.innerHTML+='<option'+(lv===val?' selected':'')+'>'+lv+'</option>';});
          fd.appendChild(sel);
        }else{
          const inp=document.createElement('input');
          inp.name=f;
          inp.type=SENSITIVE.has(f)?'password':'text';
          inp.value=SENSITIVE.has(f)&&val?'':val;
          inp.placeholder=SENSITIVE.has(f)?'••••••':'';
          fd.appendChild(inp);
        }
        div.appendChild(fd);
      }
      form.appendChild(div);
    }
  });
}

document.getElementById('btn-save').addEventListener('click',()=>{
  const form=document.getElementById('config-form');
  const data={};
  let hasRestart=false;
  form.querySelectorAll('input,select').forEach(el=>{
    if(SENSITIVE.has(el.name)&&el.value==='')return;
    data[el.name]=el.value;
    if(RESTART_FIELDS.has(el.name))hasRestart=true;
  });
  fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)})
    .then(r=>r.json()).then(()=>{
      const sb=document.getElementById('config-save-banner');
      sb.textContent='Config saved.';
      sb.classList.remove('hidden');
      setTimeout(()=>sb.classList.add('hidden'),3000);
      fetch('/api/config/reload',{method:'POST'});
      if(hasRestart){
        const b=document.getElementById('config-banner');
        b.textContent='Some changes require a restart to take effect.';
        b.classList.remove('hidden');
      }
    });
});

setInterval(()=>fetch('/api/status').then(r=>r.json()).then(updateStatus).catch(()=>{}),1000);
fetch('/api/status').then(r=>r.json()).then(updateStatus).catch(()=>{});
</script>
</body>
</html>"""


class WebDashboard:
    """Local Flask web server for status monitoring and config editing."""

    def __init__(self, config: Config, host: str = "localhost", port: int = 8080) -> None:
        self._config = config
        self._host = host
        self._port = port
        self._status = BridgeStatus()
        self._app = self._create_app()
        self._thread: Optional[threading.Thread] = None

    def _create_app(self) -> Flask:
        app = Flask(__name__)
        app.logger.setLevel(logging.WARNING)

        # Suppress Flask request logging
        wlog = logging.getLogger("werkzeug")
        wlog.setLevel(logging.WARNING)

        @app.route("/")
        def index():
            return Response(DASHBOARD_HTML, content_type="text/html")

        @app.route("/api/status")
        def api_status():
            return jsonify(self._status.to_dict())

        @app.route("/api/config", methods=["GET"])
        def api_config_get():
            cfg = self._config
            return jsonify({
                "team": cfg.team,
                "obs_host": cfg.obs_host,
                "obs_port": cfg.obs_port,
                "obs_password": cfg.obs_password,
                "stop_delay": cfg.stop_delay,
                "poll_interval": cfg.poll_interval,
                "log_level": cfg.log_level,
                "auto_teleop_gap": cfg.auto_teleop_gap,
                "nt_disconnect_grace": cfg.nt_disconnect_grace,
                "launch_on_login": cfg.launch_on_login,
                "nt_paths": ", ".join(cfg.nt_paths),
                "data_dir": str(cfg.data_dir),
                "retention_days": cfg.retention_days,
                "ravenbrain_url": cfg.ravenbrain_url,
                "ravenbrain_api_key": cfg.ravenbrain_api_key,
                "ravenbrain_batch_size": cfg.ravenbrain_batch_size,
                "ravenbrain_upload_interval": cfg.ravenbrain_upload_interval,
                "dashboard_enabled": cfg.dashboard_enabled,
                "dashboard_port": cfg.dashboard_port,
            })

        @app.route("/api/config", methods=["POST"])
        def api_config_post():
            data = request.get_json(force=True)
            cfg = self._config

            for key, val in data.items():
                if key == "team":
                    cfg.team = int(val)
                elif key == "obs_host":
                    cfg.obs_host = val
                elif key == "obs_port":
                    cfg.obs_port = int(val)
                elif key == "obs_password":
                    cfg.obs_password = val
                elif key == "stop_delay":
                    cfg.stop_delay = float(val)
                elif key == "poll_interval":
                    cfg.poll_interval = float(val)
                elif key == "log_level":
                    cfg.log_level = val
                elif key == "auto_teleop_gap":
                    cfg.auto_teleop_gap = float(val)
                elif key == "nt_disconnect_grace":
                    cfg.nt_disconnect_grace = float(val)
                elif key == "launch_on_login":
                    cfg.launch_on_login = val.lower() in ("true", "1", "yes") if isinstance(val, str) else bool(val)
                elif key == "nt_paths":
                    cfg.nt_paths = [p.strip() for p in val.split(",") if p.strip()]
                elif key == "data_dir":
                    from pathlib import Path
                    cfg.data_dir = Path(val)
                elif key == "retention_days":
                    cfg.retention_days = int(val)
                elif key == "ravenbrain_url":
                    cfg.ravenbrain_url = val
                elif key == "ravenbrain_api_key":
                    cfg.ravenbrain_api_key = val
                elif key == "ravenbrain_batch_size":
                    cfg.ravenbrain_batch_size = int(val)
                elif key == "ravenbrain_upload_interval":
                    cfg.ravenbrain_upload_interval = float(val)
                elif key == "dashboard_enabled":
                    cfg.dashboard_enabled = val.lower() in ("true", "1", "yes") if isinstance(val, str) else bool(val)
                elif key == "dashboard_port":
                    cfg.dashboard_port = int(val)

            cfg.save_to_ini()
            return jsonify({"status": "saved"})

        @app.route("/api/config/reload", methods=["POST"])
        def api_config_reload():
            changed = self._config.reload_from_ini()
            self._config.mark_changed()
            return jsonify({"status": "reloaded", "changed": changed})

        return app

    def start(self) -> None:
        self._thread = threading.Thread(
            target=lambda: self._app.run(
                host=self._host, port=self._port, debug=False, use_reloader=False,
            ),
            daemon=True,
            name="web-dashboard",
        )
        self._thread.start()
        log.info("Dashboard started at http://%s:%d", self._host, self._port)

    def update_status(self, status: BridgeStatus) -> None:
        self._status = status

    @property
    def status(self) -> BridgeStatus:
        return self._status
