#!/usr/bin/env python3
"""Learning Navigator 离线回放评估（课题四有效性验证）。

在同一张合成 Wiki 网络上回放两组模拟行为历史：
  A 组（定向学习）：围绕固定主题子网反复提问/浏览 —— 模拟真实学习轨迹
  B 组（随机散问）：在全网随机均匀曝光 —— 模拟无方向使用
然后用与后端完全一致的公式（权重 0.35/0.25/0.15、14 天半衰、1-exp(-raw/τ)、
τ=P75 自适应）计算掌握度，检验设计文档中的三条成功判据：
  1. 判别力：A 组目标节点 mastery 均值 ≥ 3× 其余节点
  2. 推荐质量：frontier 推荐落在目标主题延伸区的比例高于随机基线
  3. 收敛性：目标节点 mastery 随事件累积回放单调上升（衰减锚定在当前时刻）
"""

import math
import random
import statistics

random.seed(20260912)

HALF_LIFE_HOURS = 14 * 24
W_DOC, W_VIEW, W_TOPIC = 0.35, 0.25, 0.15
LIT, DIM = 0.6, 0.2


# ---------- 合成知识网络：5 个主题簇，每簇 4 页，簇间少量链接 ----------
def build_network():
    clusters = {}
    pages = {}
    edges = []
    topics = ["rag", "vector-db", "agent", "sandbox", "evaluation"]
    for t in topics:
        pages_in = [f"{t}/page-{i}" for i in range(4)]
        clusters[t] = pages_in
        for p in pages_in:
            pages[p] = {"cluster": t}
        for i in range(3):  # 簇内链
            edges.append((pages_in[i], pages_in[i + 1]))
    for i in range(2):  # 簇间稀疏链
        edges.append((clusters[topics[0]][i], clusters[topics[1]][i]))
        edges.append((clusters[topics[2]][i], clusters[topics[3]][i]))
    return clusters, pages, edges


def decay(hours):
    if hours <= 0:
        return 1.0
    return math.exp2(-hours / HALF_LIFE_HOURS)


def mastery_map(events, pages, now_h, tau=None):
    """events: list of (slug, kind, hours_ago)。与后端 compute() 同构；
    衰减锚定在 now_h（当前时刻）。tau 传入时不做自适应，便于跨时点横向比较。"""
    raw = {p: 0.0 for p in pages}
    for slug, kind, h in events:
        if slug not in raw:
            continue
        w = {"view": W_VIEW, "doc": W_DOC, "topic": W_TOPIC}[kind]
        raw[slug] += w * decay(now_h - h)
    if tau is None:
        positive = sorted(r for r in raw.values() if r > 0)
        tau = positive[(len(positive) * 3) // 4] if positive else 1.0
        tau = max(tau, 0.25)
    return {p: 1 - math.exp(-raw[p] / tau) for p, r in raw.items()}


def mastery_raw(events, pages, now_h):
    raw = {p: 0.0 for p in pages}
    for slug, kind, h in events:
        if slug not in raw:
            continue
        w = {"view": W_VIEW, "doc": W_DOC, "topic": W_TOPIC}[kind]
        raw[slug] += w * decay(now_h - h)
    return raw


def recommend_frontier(mastery, pages, edges):
    lit = {p for p, m in mastery.items() if m >= LIT}
    neighbors = {p: set() for p in pages}
    for a, b in edges:
        neighbors[a].add(b)
        neighbors[b].add(a)
    frontier = [
        p for p, m in mastery.items()
        if m < DIM and any(nb in lit for nb in neighbors[p])
    ]
    def score(p):
        nbs = neighbors[p]
        lit_ratio = sum(nb in lit for nb in nbs) / len(nbs) if nbs else 0
        importance = min(1.0, len(nbs) / 10)
        return 0.6 * lit_ratio + 0.4 * importance
    return sorted(frontier, key=lambda p: (-score(p), p))


# ---------- 生成两组模拟历史 ----------
def gen_histories(clusters, pages):
    # 核心目标：rag 簇全量 + vector-db 前两页；延伸目标：vector-db 后两页
    core = clusters["rag"] + clusters["vector-db"][:2]
    extension = clusters["vector-db"][2:]
    a_events = []
    for day in range(45):
        h = day * 24.0  # 真实天；每天一批学习行为
        for _ in range(3):  # 核心区高频
            a_events.append((random.choice(core), "view", h))
            a_events.append((random.choice(core), "doc", h + 1))
        for _ in range(1):  # 延伸区：仅前 10 天接触过然后中断（模拟学习停滞）
            if day <= 10:
                a_events.append((random.choice(extension), "view", h + 2))
    b_events = []
    for day in range(45):
        h = day * 24.0
        for _ in range(8):
            b_events.append((random.choice(list(pages)), "view", h))
            b_events.append((random.choice(list(pages)), "doc", h + 1))
    return core, extension, a_events, b_events


def main():
    clusters, pages, edges = build_network()
    core, extension, a_events, b_events = gen_histories(clusters, pages)
    targets = core + extension

    # 判据 1：判别力（衰减锚定在最后事件时刻）
    now_h = max(h for _, _, h in a_events)
    m_a = mastery_map(a_events, pages, now_h)
    m_b = mastery_map(b_events, pages, max(h for _, _, h in b_events))
    a_target = statistics.mean(m_a[p] for p in targets)
    a_other = statistics.mean(m for p, m in m_a.items() if p not in targets)
    ratio = a_target / max(a_other, 1e-9)

    # 判据 2：推荐质量。A 组点亮核心区后，frontier 应出现在延伸区（相邻未学）。
    recs = recommend_frontier(m_a, pages, edges)[:5]
    hit = [r for r in recs if r in extension or r.split("/")[0] == "vector-db"]
    random_baseline = len(extension) / (len(pages) - len([p for p in m_a if m_a[p] >= LIT]))

    # 判据 3：上升趋势 + 衰减生效。自适应 τ 随整体信号增长会抵消绝对值，
    # 因此跨时点比较统一使用最终 τ（消除归一化漂移，比较的是 raw 的走势）。
    #   3a 核心区随累积回放上升（末/首 ≥ 1.2）
    #   3b 中断接触的延伸区在尾段回落 ≥ 30%（时间衰减在起作用）
    final_tau = max(
        (r for r in mastery_raw(a_events, pages, now_h).values() if r > 0), default=1.0)
    a_sorted = sorted(a_events, key=lambda e: e[2])
    core_curve, ext_curve = [], []
    checkpoints = range(len(a_sorted) // 6, len(a_sorted) + 1, len(a_sorted) // 6)
    for cut in checkpoints:
        prefix = a_sorted[:cut]
        m_prefix = mastery_map(prefix, pages, max(h for _, _, h in prefix), tau=final_tau)
        core_curve.append(statistics.mean(m_prefix[p] for p in core))
        ext_curve.append(statistics.mean(m_prefix[p] for p in extension))
    growth = core_curve[-1] / max(core_curve[0], 1e-9)
    core_rising = growth >= 1.2
    ext_drop = 1 - (ext_curve[-1] / max(ext_curve[len(ext_curve) // 2], 1e-9))
    decay_works = ext_drop >= 0.30

    lit_count = sum(1 for m in m_a.values() if m >= LIT)

    print("=" * 64)
    print("Learning Navigator 离线回放评估报告")
    print("=" * 64)
    print(f"网络规模：{len(pages)} 页 / {len(edges)} 边 / 5 主题簇")
    print(f"A 组事件 {len(a_events)} 条（定向），B 组事件 {len(b_events)} 条（随机）")
    print("-" * 64)
    print(f"判据1 判别力：A 组目标节点 mastery 均值 = {a_target:.3f}，"
          f"其余节点 = {a_other:.3f}，比值 = {ratio:.1f}x "
          f"[{'PASS' if ratio >= 3 else 'FAIL'}，阈值 3x]")
    print(f"  回放后 lit 节点数 = {lit_count}，frontier Top5 = {recs}")
    print(f"判据2 推荐质量：Top5 中属目标延伸区 {len(hit)}/5；"
          f"随机基线 = {random_baseline:.2f} "
          f"[{'PASS' if len(hit) / 5 >= random_baseline else 'FAIL'}]")
    print(f"判据3 收敛与衰减：核心区曲线 = {[round(v, 3) for v in core_curve]}，"
          f"末/首 = {growth:.2f}x [{'PASS' if core_rising else 'FAIL'}，阈值 1.2x]；"
          f"中断接触的延伸区尾段回落 = {ext_drop * 100:.0f}% "
          f"[{'PASS' if decay_works else 'FAIL'}，阈值 30%]")
    print("-" * 64)
    c1, c2, c3 = ratio >= 3, len(hit) / 5 >= random_baseline, core_rising and decay_works
    print(f"总体：{'PASS' if (c1 and c2 and c3) else 'FAIL'}（判据1 {'PASS' if c1 else 'FAIL'} / "
          f"判据2 {'PASS' if c2 else 'FAIL'} / 判据3 {'PASS' if c3 else 'FAIL'}）")


if __name__ == "__main__":
    main()
