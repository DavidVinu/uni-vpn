// Runs worker.js on a local port with an in-memory database, for the Go tests against the
// real relay code: node relay/serve.mjs prints the base URL.
import {createServer} from "node:http";
import {handle} from "./worker.js";
import {d1} from "./d1.mjs";

const env = {DB: d1()};
const server = createServer(async (req, res) => {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  const body = Buffer.concat(chunks);
  const request = new Request("http://relay" + req.url, {method: req.method, headers: req.headers,
    body: ["GET", "HEAD"].includes(req.method) ? undefined : body});
  const response = await handle(request, env);
  res.writeHead(response.status, Object.fromEntries(response.headers));
  res.end(Buffer.from(await response.arrayBuffer()));
});
server.listen(Number(process.env.PORT || 0), "127.0.0.1", () => {
  console.log(`http://127.0.0.1:${server.address().port}`);
});
