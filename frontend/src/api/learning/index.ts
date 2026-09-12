import { get, post, del } from "../../utils/request";

export interface LearningSignals {
  doc: number;
  view: number;
  topic: number;
}

export interface LearningNode {
  slug: string;
  title: string;
  page_type: string;
  mastery: number;
  state: "lit" | "dim" | "frontier" | "dark";
  in_links: number;
  out_links: number;
  lit_in: number;
  lit_out: number;
  signals: LearningSignals;
}

export interface LearningRecommendation {
  slug: string;
  title: string;
  mastery: number;
  score: number;
  reason: string;
  lit_neighbors: number;
}

export interface LearningProfile {
  knowledge_base_id: string;
  nodes: LearningNode[];
  recommendations: LearningRecommendation[];
  computed_at: string;
}

// getLearningProfile computes (and persists) the caller's mastery profile for
// one knowledge base, with recommendations attached.
export function getLearningProfile(kbId: string) {
  return get(`/api/v1/learning/profile?kb_id=${encodeURIComponent(kbId)}`);
}

export function getLearningRecommendations(kbId: string, limit = 5) {
  return get(
    `/api/v1/learning/recommendations?kb_id=${encodeURIComponent(kbId)}&limit=${limit}`
  );
}

export function recordLearningView(kbId: string, slug: string) {
  return post(`/api/v1/learning/events/view`, { kb_id: kbId, slug });
}

export function deleteLearningProfile(kbId?: string) {
  const qs = kbId ? `?kb_id=${encodeURIComponent(kbId)}` : "";
  return del(`/api/v1/learning/profile${qs}`);
}
