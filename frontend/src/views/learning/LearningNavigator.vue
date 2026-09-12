<template>
  <div class="learning-navigator">
    <div class="page-header">
      <h2>学习导航</h2>
      <p class="subtitle">知识网络掌握度画像：点亮你已掌握的节点，发现下一步该学什么</p>
    </div>

    <div class="toolbar">
      <t-select
        v-model="selectedKb"
        placeholder="选择 Wiki 知识库"
        filterable
        :loading="kbLoading"
        style="width: 320px"
        @change="loadProfile"
      >
        <t-option
          v-for="kb in kbList"
          :key="kb.id"
          :value="kb.id"
          :label="kb.name"
        />
      </t-select>
      <t-button variant="outline" :disabled="!selectedKb || loading" @click="loadProfile">
        刷新画像
      </t-button>
      <t-popconfirm content="确定删除本知识库的学习画像吗？该操作不可恢复。" @confirm="removeProfile">
        <t-button variant="outline" theme="danger" :disabled="!selectedKb">删除画像</t-button>
      </t-popconfirm>
    </div>

    <t-alert v-if="error" theme="error" :message="error" style="margin-bottom: 12px" />

    <div v-if="profile" class="content">
      <t-card class="legend-card" :bordered="false">
        <div class="legend">
          <span v-for="item in legend" :key="item.state" class="legend-item">
            <i class="dot" :style="{ background: item.color }" />{{ item.label }}
          </span>
          <span class="legend-note">掌握度由可观测行为（回答引用 / 页面浏览 / 兴趣主题）衰减加权得出，不依赖模型打分</span>
        </div>
      </t-card>

      <div class="main-grid">
        <t-card title="知识网络" :bordered="false">
          <div ref="graphWrap" class="graph-wrap">
            <svg :width="graphWidth" :height="graphHeight" @mousemove="onDrag" @mouseup="endDrag">
              <g>
                <line
                  v-for="(e, i) in graphEdges"
                  :key="'e' + i"
                  :x1="nodePos[e.source]?.x" :y1="nodePos[e.source]?.y"
                  :x2="nodePos[e.target]?.x" :y2="nodePos[e.target]?.y"
                  class="edge"
                />
              </g>
              <g
                v-for="n in graphNodes"
                :key="n.slug"
                :transform="`translate(${nodePos[n.slug]?.x || 0},${nodePos[n.slug]?.y || 0})`"
                class="node"
                @mousedown="startDrag(n.slug, $event)"
                @click.stop="openPage(n)"
              >
                <circle :r="nodeRadius(n)" :fill="stateColor(n.state)" :class="{ lit: n.state === 'lit' }" />
                <text :y="nodeRadius(n) + 12" class="label">{{ shortTitle(n.title) }}</text>
              </g>
            </svg>
            <div v-if="!graphNodes.length" class="empty">该知识库暂无 Wiki 页面或尚未生成图谱</div>
          </div>
        </t-card>

        <t-card title="下一步学什么" :bordered="false">
          <div class="recs">
            <div
              v-for="rec in profile.recommendations"
              :key="rec.slug"
              class="rec-card"
              @click="openBySlug(rec.slug)"
            >
              <div class="rec-title">{{ rec.title }}</div>
              <div class="rec-reason">{{ rec.reason }}（{{ rec.lit_neighbors }} 个相邻节点已点亮）</div>
            </div>
            <div v-if="!profile.recommendations.length" class="empty">
              暂无推荐：继续在对话中使用该知识库，或浏览 Wiki 页面
            </div>
          </div>
        </t-card>
      </div>

      <t-card title="节点明细" :bordered="false" style="margin-top: 16px">
        <t-table
          :data="sortedNodes"
          :columns="columns"
          row-key="slug"
          size="small"
          :pagination="{ pageSize: 10 }"
        />
      </t-card>
    </div>
    <t-loading v-else :loading="loading" text="画像计算中…">
      <div class="placeholder" />
    </t-loading>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { MessagePlugin } from "tdesign-vue-next";
import { listKnowledgeBases } from "@/api/knowledge-base";
import {
  deleteLearningProfile,
  getLearningProfile,
  recordLearningView,
  type LearningNode,
  type LearningProfile,
} from "@/api/learning";
import { getWikiGraph } from "@/api/wiki";

const kbList = ref<{ id: string; name: string }[]>([]);
const kbLoading = ref(false);
const selectedKb = ref("");
const profile = ref<LearningProfile | null>(null);
const loading = ref(false);
const error = ref("");

const legend = [
  { state: "lit", label: "已点亮", color: "#2ba471" },
  { state: "frontier", label: "前沿（推荐优先）", color: "#ed7b2f" },
  { state: "dim", label: "接触中", color: "#8884d8" },
  { state: "dark", label: "未触及", color: "#c8c8c8" },
];

const stateColor = (state: string) =>
  legend.find(l => l.state === state)?.color || "#c8c8c8";

const columns = [
  { colKey: "title", title: "页面", ellipsis: true },
  { colKey: "page_type", title: "类型", width: 100 },
  { colKey: "mastery", title: "掌握度", width: 90 },
  { colKey: "state", title: "状态", width: 90 },
  { colKey: "signals.doc", title: "引用信号", width: 100 },
  { colKey: "signals.view", title: "浏览信号", width: 100 },
  { colKey: "signals.topic", title: "主题信号", width: 100 },
];

const sortedNodes = computed(() =>
  [...(profile.value?.nodes || [])].sort((a, b) => b.mastery - a.mastery)
);

async function loadKbs() {
  kbLoading.value = true;
  try {
    const res: any = await listKnowledgeBases();
    kbList.value = (res?.data || res || []).map((kb: any) => ({
      id: kb.ID || kb.id,
      name: kb.Name || kb.name,
    }));
  } catch (e: any) {
    error.value = e?.message || "知识库列表加载失败";
  } finally {
    kbLoading.value = false;
  }
}

async function loadProfile() {
  if (!selectedKb.value) return;
  loading.value = true;
  error.value = "";
  try {
    const res: any = await getLearningProfile(selectedKb.value);
    profile.value = res?.data || res;
    await loadGraph();
    runLayout();
  } catch (e: any) {
    error.value = e?.message || "画像加载失败";
  } finally {
    loading.value = false;
  }
}

async function removeProfile() {
  try {
    await deleteLearningProfile(selectedKb.value);
    MessagePlugin.success("学习画像已删除");
    profile.value = null;
  } catch (e: any) {
    MessagePlugin.error(e?.message || "删除失败");
  }
}

async function openPage(n: { slug: string }) {
  await openBySlug(n.slug);
}

async function openBySlug(slug: string) {
  try {
    await recordLearningView(selectedKb.value, slug);
    MessagePlugin.success(`已记录学习：${slug}`);
    await loadProfile();
  } catch {
    /* 记录失败不打断浏览 */
  }
}

// ---- graph ----
const graphNodes = ref<{ slug: string; title: string; state: string; mastery: number }[]>([]);
const graphEdges = ref<{ source: string; target: string }[]>([]);
const graphWidth = 640;
const graphHeight = 480;
const graphWrap = ref<HTMLElement>();
const nodePos = reactive<Record<string, { x: number; y: number }>>({});
let dragging = "";
let simTimer: number | null = null;

async function loadGraph() {
  graphNodes.value = [];
  graphEdges.value = [];
  try {
    const res: any = await getWikiGraph(selectedKb.value, { mode: "overview", limit: 120 });
    const data = res?.data || res;
    const masteryBySlug = new Map(
      (profile.value?.nodes || []).map((n: any) => [n.slug, n])
    );
    graphNodes.value = (data?.nodes || []).map((gn: any) => {
      const m = masteryBySlug.get(gn.slug);
      return {
        slug: gn.slug,
        title: gn.title,
        state: m?.state || "dark",
        mastery: m?.mastery || 0,
      };
    });
    graphEdges.value = data?.edges || [];
  } catch {
    /* 图谱缺失时仅隐藏可视化 */
  }
}

function nodeRadius(n: { mastery: number }) {
  return 6 + n.mastery * 10;
}

function shortTitle(title: string) {
  return title.length > 12 ? title.slice(0, 11) + "…" : title;
}

// Lightweight force simulation: repulsion + spring + centering. Deterministic
// and dependency-free, mirroring the repo's no-graph-library convention.
function runLayout() {
  const nodes = graphNodes.value;
  if (!nodes.length) return;
  const rand = mulberry32(42);
  nodes.forEach(n => {
    nodePos[n.slug] = {
      x: graphWidth / 2 + (rand() - 0.5) * graphWidth * 0.6,
      y: graphHeight / 2 + (rand() - 0.5) * graphHeight * 0.6,
    };
  });
  const adj = new Map<string, string[]>();
  graphEdges.value.forEach(e => {
    if (!nodePos[e.source] || !nodePos[e.target]) return;
    adj.set(e.source, [...(adj.get(e.source) || []), e.target]);
    adj.set(e.target, [...(adj.get(e.target) || []), e.source]);
  });
  let alpha = 1;
  if (simTimer) window.clearInterval(simTimer);
  simTimer = window.setInterval(() => {
    alpha *= 0.97;
    for (const a of nodes) {
      const pa = nodePos[a.slug];
      let fx = 0;
      let fy = 0;
      for (const b of nodes) {
        if (a.slug === b.slug) continue;
        const pb = nodePos[b.slug];
        const dx = pa.x - pb.x;
        const dy = pa.y - pb.y;
        const d2 = Math.max(dx * dx + dy * dy, 100);
        fx += (dx / d2) * 800;
        fy += (dy / d2) * 800;
      }
      for (const b of adj.get(a.slug) || []) {
        const pb = nodePos[b];
        fx += (pb.x - pa.x) * 0.02;
        fy += (pb.y - pa.y) * 0.02;
      }
      fx += (graphWidth / 2 - pa.x) * 0.01;
      fy += (graphHeight / 2 - pa.y) * 0.01;
      pa.x = Math.min(graphWidth - 20, Math.max(20, pa.x + fx * alpha));
      pa.y = Math.min(graphHeight - 20, Math.max(20, pa.y + fy * alpha));
    }
    if (alpha < 0.01 && simTimer) {
      window.clearInterval(simTimer);
      simTimer = null;
    }
  }, 16);
}

function mulberry32(seed: number) {
  return () => {
    seed |= 0;
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function startDrag(slug: string, ev: MouseEvent) {
  dragging = slug;
  ev.preventDefault();
}
function onDrag(ev: MouseEvent) {
  if (!dragging || !nodePos[dragging]) return;
  const rect = graphWrap.value?.getBoundingClientRect();
  if (!rect) return;
  nodePos[dragging].x = ev.clientX - rect.left;
  nodePos[dragging].y = ev.clientY - rect.top;
}
function endDrag() {
  dragging = "";
}

onMounted(loadKbs);
onBeforeUnmount(() => {
  if (simTimer) window.clearInterval(simTimer);
});
</script>

<style scoped lang="less">
.learning-navigator {
  padding: 24px;
  max-width: 1200px;
  margin: 0 auto;
}
.page-header h2 {
  margin: 0 0 4px;
}
.subtitle {
  color: var(--td-text-color-secondary, #666);
  margin: 0 0 16px;
  font-size: 13px;
}
.toolbar {
  display: flex;
  gap: 12px;
  margin-bottom: 16px;
}
.legend {
  display: flex;
  gap: 16px;
  align-items: center;
  flex-wrap: wrap;
  font-size: 13px;
  .legend-item {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .legend-note {
    color: #999;
  }
}
.dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  display: inline-block;
}
.main-grid {
  display: grid;
  grid-template-columns: 2fr 1fr;
  gap: 16px;
}
.graph-wrap {
  position: relative;
}
.edge {
  stroke: #ddd;
  stroke-width: 1;
}
.node {
  cursor: pointer;
  circle.lit {
    stroke: #1f6e46;
    stroke-width: 2;
  }
  .label {
    font-size: 11px;
    text-anchor: middle;
    fill: #555;
    user-select: none;
  }
}
.recs {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.rec-card {
  border: 1px solid #eee;
  border-left: 3px solid #ed7b2f;
  border-radius: 6px;
  padding: 10px 12px;
  cursor: pointer;
  &:hover {
    background: #fafafa;
  }
  .rec-title {
    font-weight: 600;
    margin-bottom: 4px;
  }
  .rec-reason {
    font-size: 12px;
    color: #777;
  }
}
.empty {
  color: #999;
  text-align: center;
  padding: 40px 0;
}
.placeholder {
  height: 200px;
}
</style>
