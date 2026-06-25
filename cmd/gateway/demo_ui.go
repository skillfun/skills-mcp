package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const demoPageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>SkillFun MCP Demo</title>
  <style>
    :root {
      color-scheme: light dark;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    body {
      margin: 0;
      background: #0f172a;
      color: #e2e8f0;
    }
    main {
      max-width: 1200px;
      margin: 0 auto;
      padding: 24px;
    }
    h1, h2 {
      margin-top: 0;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
      gap: 16px;
    }
    .card {
      background: #111827;
      border: 1px solid #334155;
      border-radius: 12px;
      padding: 16px;
      box-shadow: 0 8px 24px rgba(15, 23, 42, 0.24);
    }
    label {
      display: block;
      font-size: 14px;
      margin-bottom: 6px;
      color: #cbd5e1;
    }
    input, textarea, select, button {
      width: 100%;
      box-sizing: border-box;
      border-radius: 8px;
      border: 1px solid #475569;
      background: #020617;
      color: #e2e8f0;
      padding: 10px 12px;
      margin-bottom: 12px;
      font: inherit;
    }
    textarea {
      min-height: 110px;
      resize: vertical;
    }
    button {
      cursor: pointer;
      background: #2563eb;
      border: none;
      font-weight: 600;
    }
    button.secondary {
      background: #334155;
    }
    .row {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 12px;
    }
    pre {
      margin: 0;
      white-space: pre-wrap;
      word-break: break-word;
      background: #020617;
      border: 1px solid #334155;
      border-radius: 8px;
      padding: 12px;
      min-height: 180px;
      overflow: auto;
    }
    .hint {
      color: #94a3b8;
      font-size: 13px;
      margin-bottom: 12px;
    }
    .status {
      margin-bottom: 16px;
      color: #93c5fd;
    }
  </style>
</head>
<body>
  <main>
    <h1>SkillFun MCP Demo</h1>
    <p class="status" id="status">Ready.</p>
    <div class="grid">
      <section class="card">
        <h2>1. 创建 Demo Bundle</h2>
        <p class="hint">这里只需要 admin token。创建成功后，右侧 MCP 调试区会直接复用 subdomain。</p>
        <label for="adminToken">BUNDLE_ADMIN_TOKEN</label>
        <input id="adminToken" type="text" value="demo-admin-token">
        <div class="row">
          <div>
            <label for="bundleName">bundleName</label>
            <input id="bundleName" type="text" value="demo">
          </div>
          <div>
            <label for="subdomain">subdomain</label>
            <input id="subdomain" type="text" value="demo2025">
          </div>
        </div>
        <label for="displayName">displayName</label>
        <input id="displayName" type="text" value="Demo Bundle">
        <label for="bundleDescription">description</label>
        <input id="bundleDescription" type="text" value="Local demo bundle for MCP flow">
        <div class="row">
          <div>
            <label for="nftId">nftId</label>
            <input id="nftId" type="number" value="1001">
          </div>
          <div>
            <label for="skillName">skill name</label>
            <input id="skillName" type="text" value="demo.current">
          </div>
        </div>
        <label for="skillDescription">skill description</label>
        <input id="skillDescription" type="text" value="Demo skill backed by a public GitHub folder">
        <label for="githubUrl">githubUrl</label>
        <input id="githubUrl" type="text" value="https://github.com/github/gitignore/tree/main/.github">
        <label for="inputSchema">inputSchema JSON</label>
        <textarea id="inputSchema">{
  "type": "object",
  "properties": {
    "city": {
      "type": "string"
    }
  },
  "required": ["city"]
}</textarea>
        <button id="createBundle">创建 / 覆盖 Bundle</button>
        <pre id="createOutput">尚未请求</pre>
      </section>

      <section class="card">
        <h2>2. 查询与 MCP 调试</h2>
        <label for="querySubdomain">bundle subdomain</label>
        <input id="querySubdomain" type="text" value="demo2025">
        <div class="row">
          <button class="secondary" id="loadBundles">GET /v1/mcp/bundles</button>
          <button class="secondary" id="loadSkills">GET /v1/mcp/bundles/:subdomain/skills</button>
        </div>
        <div class="row">
          <button class="secondary" id="loadTools">MCP tools/list</button>
          <button class="secondary" id="loadResources">MCP resources/list</button>
        </div>
        <label for="resourceURI">resource URI</label>
        <select id="resourceURI"></select>
        <button class="secondary" id="readResource">MCP resources/read</button>
        <pre id="queryOutput">尚未请求</pre>
      </section>
    </div>
  </main>
  <script>
    const el = (id) => document.getElementById(id);

    function setStatus(message, isError = false) {
      const node = el("status");
      node.textContent = message;
      node.style.color = isError ? "#fca5a5" : "#93c5fd";
    }

    function setOutput(id, value) {
      if (typeof value === "string") {
        el(id).textContent = value;
        return;
      }
      el(id).textContent = JSON.stringify(value, null, 2);
    }

    function adminHeaders() {
      const token = el("adminToken").value.trim();
      return token ? { Authorization: "Bearer " + token } : {};
    }

    function currentSubdomain() {
      return el("querySubdomain").value.trim() || el("subdomain").value.trim();
    }

    async function request(method, path, body, extraHeaders = {}) {
      setStatus(method + " " + path + " ...");
      const headers = { ...extraHeaders };
      if (body !== undefined) {
        headers["Content-Type"] = "application/json";
      }

      const response = await fetch(path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
      });

      const text = await response.text();
      let payload = text;
      if (text) {
        try {
          payload = JSON.parse(text);
        } catch (_) {
          payload = text;
        }
      }

      if (!response.ok) {
        const detail = typeof payload === "string" ? payload : JSON.stringify(payload, null, 2);
        throw new Error("HTTP " + response.status + "\\n" + detail);
      }

      setStatus(method + " " + path + " 完成");
      return payload;
    }

    function updateResourceOptions(resources) {
      const select = el("resourceURI");
      select.innerHTML = "";
      for (const resource of resources || []) {
        const option = document.createElement("option");
        option.value = resource.uri;
        option.textContent = resource.name + " — " + resource.uri;
        select.appendChild(option);
      }
    }

    async function createBundle() {
      let inputSchema;
      try {
        inputSchema = JSON.parse(el("inputSchema").value);
      } catch (error) {
        setStatus("inputSchema 不是合法 JSON", true);
        setOutput("createOutput", String(error));
        return;
      }

      const payload = {
        bundleName: el("bundleName").value.trim(),
        subdomain: el("subdomain").value.trim(),
        displayName: el("displayName").value.trim(),
        description: el("bundleDescription").value.trim(),
        skills: [
          {
            nftId: Number(el("nftId").value),
            name: el("skillName").value.trim(),
            description: el("skillDescription").value.trim(),
            inputSchema,
            githubUrl: el("githubUrl").value.trim(),
          },
        ],
      };

      try {
        const result = await request("POST", "/v1/mcp/bundles", payload, adminHeaders());
        el("querySubdomain").value = payload.subdomain;
        setOutput("createOutput", result);
        await loadBundles();
      } catch (error) {
        setStatus("创建 bundle 失败", true);
        setOutput("createOutput", String(error));
      }
    }

    async function loadBundles() {
      try {
        const result = await request("GET", "/v1/mcp/bundles");
        setOutput("queryOutput", result);
      } catch (error) {
        setStatus("查询 bundles 失败", true);
        setOutput("queryOutput", String(error));
      }
    }

    async function loadSkills() {
      const subdomain = currentSubdomain();
      try {
        const result = await request("GET", "/v1/mcp/bundles/" + encodeURIComponent(subdomain) + "/skills");
        setOutput("queryOutput", result);
      } catch (error) {
        setStatus("查询 bundle skills 失败", true);
        setOutput("queryOutput", String(error));
      }
    }

    async function callMCP(method, params) {
      const subdomain = currentSubdomain();
      return request("POST", "/" + encodeURIComponent(subdomain) + "/mcp", {
        jsonrpc: "2.0",
        id: Date.now(),
        method,
        params,
      });
    }

    async function loadTools() {
      try {
        const result = await callMCP("tools/list", {});
        setOutput("queryOutput", result);
      } catch (error) {
        setStatus("tools/list 失败", true);
        setOutput("queryOutput", String(error));
      }
    }

    async function loadResources() {
      try {
        const result = await callMCP("resources/list", {});
        const resources = result && result.result ? result.result.resources || [] : [];
        updateResourceOptions(resources);
        setOutput("queryOutput", result);
      } catch (error) {
        setStatus("resources/list 失败", true);
        setOutput("queryOutput", String(error));
      }
    }

    async function readResource() {
      const uri = el("resourceURI").value.trim();
      if (!uri) {
        setStatus("先执行 resources/list，再选择一个 resource URI", true);
        return;
      }

      try {
        const result = await callMCP("resources/read", { uri });
        setOutput("queryOutput", result);
      } catch (error) {
        setStatus("resources/read 失败", true);
        setOutput("queryOutput", String(error));
      }
    }

    el("createBundle").addEventListener("click", createBundle);
    el("loadBundles").addEventListener("click", loadBundles);
    el("loadSkills").addEventListener("click", loadSkills);
    el("loadTools").addEventListener("click", loadTools);
    el("loadResources").addEventListener("click", loadResources);
    el("readResource").addEventListener("click", readResource);
  </script>
</body>
</html>
`

func handleDemoPage() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(demoPageHTML))
	}
}
