// Test the full normalizeModelName + aggregateModels pipeline with real API data
function normalizeModelName(model) {
  if (!model) return "unknown";
  var s = model.toLowerCase().trim();
  s = s.replace(/^[a-z0-9_-]+\//, "");
  s = s.replace(/[：(\uff08]free[\uff09)]?$/, "");
  s = s.replace(/:free$/, "");
  s = s.replace(/-free$/, "");
  s = s.replace(/-low$/, "");
  s = s.replace(/-thinking$/, "");
  s = s.replace(/-\d{4}-\d{2}-\d{2}$/, "");
  s = s.replace(/-\d{8}$/, "");
  s = s.replace(/-20\d{6}$/, "");
  s = s.replace(/-preview(-\d{4}-\d{2}-\d{2})?$/, "");
  s = s.replace(/-latest$/, "");
  // Normalize by inserting brand-version hyphen for fuzzy merge: grok4.5 → grok-4.5
  s = s.replace(/^([a-z]+)(\d+(?:\.\d+)?)$/, "$1-$2");
  return s;
}

function aggregateModels(models) {
  var groups = {};
  models.forEach(function(m) {
    var key = normalizeModelName(m.model);
    if (!groups[key]) {
      groups[key] = {
        provider: m.provider || "",
        model: key,
        requests: 0,
        input_tokens: 0,
        output_tokens: 0,
        total_tokens: 0,
        cached_tokens: 0,
        cost: 0,
        count: 0,
        childModels: []
      };
    }
    var g = groups[key];
    g.requests += Number(m.requests) || 0;
    g.input_tokens += Number(m.input_tokens) || 0;
    g.output_tokens += Number(m.output_tokens) || 0;
    g.total_tokens += Number(m.total_tokens) || 0;
    g.cached_tokens += Number(m.cached_tokens) || 0;
    g.cost += Number(m.cost) || 0;
    g.count++;
    g.childModels.push(m.model);
  });
  return Object.values(groups).sort(function(a, b) { return b.total_tokens - a.total_tokens; });
}

// Simulated real API data
const apiData = [
  { model: "deepseek-ai/deepseek-v4-flash", provider: "deepseek", requests: 8, input_tokens: 10000, output_tokens: 5000, total_tokens: 15000, cached_tokens: 0, cost: 0 },
  { model: "deepseek-v4-flash", provider: "multiple", requests: 1260, input_tokens: 500000, output_tokens: 200000, total_tokens: 700000, cached_tokens: 0, cost: 0.9334 },
  { model: "deepseek-v4-flash-free", provider: "multiple", requests: 5, input_tokens: 2000, output_tokens: 1000, total_tokens: 3000, cached_tokens: 0, cost: 0 },
  { model: "deepseek-v4-flash:free", provider: "multiple", requests: 3, input_tokens: 1000, output_tokens: 500, total_tokens: 1500, cached_tokens: 0, cost: 0 },
  { model: "deepseek-v4-pro", provider: "multiple", requests: 4163, input_tokens: 3000000, output_tokens: 1500000, total_tokens: 4500000, cached_tokens: 0, cost: 30.8104 },
  { model: "deepseek/deepseek-v4-flash", provider: "deepseek", requests: 88, input_tokens: 40000, output_tokens: 20000, total_tokens: 60000, cached_tokens: 0, cost: 0 },
  { model: "deepseek/deepseek-v4-pro", provider: "deepseek", requests: 336, input_tokens: 300000, output_tokens: 100000, total_tokens: 400000, cached_tokens: 0, cost: 0 },
  { model: "gemini-2.5-pro", provider: "multiple", requests: 28, input_tokens: 5000, output_tokens: 2000, total_tokens: 7000, cached_tokens: 0, cost: 0 },
  { model: "gemini-3-flash-preview", provider: "multiple", requests: 6, input_tokens: 1000, output_tokens: 500, total_tokens: 1500, cached_tokens: 0, cost: 0 },
  { model: "gemini-3-pro-preview", provider: "multiple", requests: 32, input_tokens: 10000, output_tokens: 5000, total_tokens: 15000, cached_tokens: 0, cost: 0 },
  { model: "gemini-3.1-pro", provider: "multiple", requests: 13, input_tokens: 5000, output_tokens: 2000, total_tokens: 7000, cached_tokens: 0, cost: 0 },
  { model: "gemini-3.1-pro-low", provider: "multiple", requests: 2, input_tokens: 500, output_tokens: 200, total_tokens: 700, cached_tokens: 0, cost: 0 },
  { model: "gemini-3.1-pro-preview", provider: "multiple", requests: 42, input_tokens: 20000, output_tokens: 10000, total_tokens: 30000, cached_tokens: 0, cost: 0 },
  { model: "gemini-3.5-flash", provider: "multiple", requests: 1403, input_tokens: 800000, output_tokens: 400000, total_tokens: 1200000, cached_tokens: 0, cost: 244.4094 },
  { model: "gemini-3.5-flash-low", provider: "multiple", requests: 53, input_tokens: 20000, output_tokens: 8000, total_tokens: 28000, cached_tokens: 0, cost: 0 },
  { model: "gemini-3.5-flash-thinking", provider: "multiple", requests: 37, input_tokens: 15000, output_tokens: 10000, total_tokens: 25000, cached_tokens: 0, cost: 0 },
  { model: "glm-5.2", provider: "multiple", requests: 629, input_tokens: 400000, output_tokens: 200000, total_tokens: 600000, cached_tokens: 0, cost: 7.9037 },
  { model: "google/gemini-3.1-pro-preview", provider: "google", requests: 1, input_tokens: 500, output_tokens: 200, total_tokens: 700, cached_tokens: 0, cost: 0 },
  { model: "google/gemini-3.5-flash", provider: "google", requests: 45, input_tokens: 30000, output_tokens: 15000, total_tokens: 45000, cached_tokens: 0, cost: 0 },
  { model: "gpt-5.4", provider: "multiple", requests: 5, input_tokens: 10000, output_tokens: 5000, total_tokens: 15000, cached_tokens: 0, cost: 0.1175 },
  { model: "gpt-5.5", provider: "multiple", requests: 53, input_tokens: 80000, output_tokens: 40000, total_tokens: 120000, cached_tokens: 0, cost: 2.1488 },
  { model: "gpt-5.6-sol", provider: "multiple", requests: 3761, input_tokens: 5000000, output_tokens: 2500000, total_tokens: 7500000, cached_tokens: 0, cost: 874.8781 },
  { model: "grok-4.5", provider: "multiple", requests: 42, input_tokens: 50000, output_tokens: 20000, total_tokens: 70000, cached_tokens: 0, cost: 5.9648 },
  { model: "grok4.5\uff08free\uff09", provider: "multiple", requests: 21, input_tokens: 5000, output_tokens: 2000, total_tokens: 7000, cached_tokens: 0, cost: 0 },
  { model: "kimi-k2.5", provider: "multiple", requests: 2, input_tokens: 1000, output_tokens: 500, total_tokens: 1500, cached_tokens: 0, cost: 0.0001 },
  { model: "kimi-k2.6", provider: "multiple", requests: 142, input_tokens: 100000, output_tokens: 40000, total_tokens: 140000, cached_tokens: 0, cost: 4.0857 },
  { model: "moonshotai/kimi-k2.6", provider: "moonshotai", requests: 25, input_tokens: 15000, output_tokens: 5000, total_tokens: 20000, cached_tokens: 0, cost: 0 },
];

console.log("=== Detailed (raw) view: " + apiData.length + " models ===");
apiData.sort((a,b) => b.total_tokens - a.total_tokens).forEach(m => {
  console.log("  " + m.model.padEnd(35) + " req=" + String(m.requests).padStart(5) + " cost=$" + m.cost.toFixed(2));
});

console.log("\n=== Aggregated view ===");
const aggregated = aggregateModels(apiData);
aggregated.forEach(g => {
  const children = g.count > 1 ? " [" + g.childModels.join(", ") + "]" : "";
  console.log("  " + g.model.padEnd(30) + " req=" + String(g.requests).padStart(5) + " cost=$" + g.cost.toFixed(4) + "  (" + g.count + " models)" + children);
});

console.log("\n=== Merge summary ===");
console.log("Raw count: " + apiData.length + " → Aggregated count: " + aggregated.length);
console.log("Reduction: " + (apiData.length - aggregated.length) + " merged away");
