import { describe, expect, test } from "bun:test";
import { continuousVideoStarters, createContinuousVideoWorkflow } from "../src/lib/canvas/continuous-video-workflow";
import { validateWorkflowDraft, workflowEditableFields } from "../src/lib/canvas/workflow-editor";
import { validateWorkflowPromptMentions } from "../src/lib/canvas/workflow-node-mentions";
import { createWorkflowInstance } from "../src/lib/canvas/reusable-workflow-instance";
import type { WorkflowOperation } from "../src/services/api/canvas-workflows";

const operations: WorkflowOperation[] = [
    { id: "generate.video", version: 1, name: "生成视频", outputType: "video", inputs: [
        { id: "referenceImages", label: "参考图片", mediaType: "image", required: false, multiple: true },
        { id: "referenceVideos", label: "参考视频", mediaType: "video", required: false, multiple: true },
    ], fields: [
        { key: "modelOption", label: "模型", type: "select", options: [{ value: "enabled-channel::chosen-video", label: "可选视频模型" }] },
        { key: "videoEditOperation", label: "素材使用方式", type: "select", options: [{ value: "", label: "自动" }, { value: "reference_to_video", label: "参考素材" }] },
    ] },
    { id: "video.frame", version: 1, name: "截取视频帧", outputType: "image", inputs: [{ id: "video", label: "视频", mediaType: "video", required: true, multiple: false }], fields: [{ key: "mode", label: "截帧方式", type: "select", options: [{ value: "last", label: "实际末帧" }] }] },
];

describe("continuous video workflow starters", () => {
    test("all entries are ordinary valid workflow drafts with connected prompt references", () => {
        for (const starter of continuousVideoStarters) {
            const { definition } = createContinuousVideoWorkflow(starter.stage, operations);
            expect(() => validateWorkflowDraft(definition, operations)).not.toThrow();
            expect(() => validateWorkflowPromptMentions(definition)).not.toThrow();
            expect(definition.presentationVersion).toBe(1);
            expect(definition.nodes.every(node => !node.resourceId && !node.sourceTaskId)).toBe(true);
            expect(definition.nodes.filter(node => node.role === "input").every(node => !node.useDefault)).toBe(true);
            for (const node of definition.nodes.filter(node => node.operationId === "generate.video")) {
                expect(node.config?.modelOption).toBeUndefined();
                expect(node.config?.videoEditOperation).toBe("reference_to_video");
                expect(node.config?.apiKey).toBeUndefined();
            }
        }
    });

    test("second part depends on the generated first part's actual last frame with distinct reference slots", () => {
        const { definition } = createContinuousVideoWorkflow("complete", operations);
        expect(definition.nodes.filter(node => node.role === "input").map(node => node.mediaType)).toEqual(["image", "video", "video"]);
        expect(definition.nodes.find(node => node.id === "first-video")?.config?.videoSeconds).toBe(11);
        expect(definition.nodes.find(node => node.id === "second-video")?.config?.videoSeconds).toBe(10);
        expect(definition.nodes.find(node => node.id === "last-frame")?.config).toEqual({ mode: "last" });
        expect(definition.edges.find(edge => edge.toNodeId === "last-frame")).toEqual({ fromNodeId: "first-video", toNodeId: "last-frame", fromPort: "output", toPort: "video", order: 0 });
        expect(definition.edges.filter(edge => edge.toNodeId === "second-video").map(edge => [edge.fromNodeId, edge.toPort, edge.order])).toEqual([
            ["character", "referenceImages", 0], ["last-frame", "referenceImages", 1], ["second-motion", "referenceVideos", 0],
        ]);
        expect(definition.outputNodeIds).toEqual(["first-video", "last-frame", "second-video"]);
    });

    test("stage variants allow freezing the first part and rerunning only the continuation", () => {
        const first = createContinuousVideoWorkflow("first", operations).definition;
        expect(first.nodes.filter(node => node.operationId === "generate.video").map(node => node.id)).toEqual(["first-video"]);
        expect(first.outputNodeIds).toEqual(["first-video", "last-frame"]);
        const continuation = createContinuousVideoWorkflow("continue", operations).definition;
        expect(continuation.nodes.filter(node => node.operationId === "generate.video").map(node => node.id)).toEqual(["second-video"]);
        expect(continuation.nodes.find(node => node.id === "first-video")?.role).toBe("input");
        expect(continuation.nodes.some(node => node.id === "first-motion")).toBe(false);
        expect(continuation.edges.some(edge => edge.fromNodeId === "first-video" && edge.toNodeId === "last-frame")).toBe(true);
        expect(continuation.outputNodeIds).toEqual(["last-frame", "second-video"]);
    });

    test("saved starter instantiates with editable inputs and the usual workflow control", () => {
        const draft = createContinuousVideoWorkflow("complete", operations);
        const graph = createWorkflowInstance({ ...draft, id: "saved-template", revision: 1, createdAt: "", updatedAt: "" }, { x: 0, y: 0 });
        const workflow = graph.control.metadata?.reusableWorkflow;
        expect(workflow?.templateId).toBe("saved-template");
        expect(workflow?.templateRevision).toBe(1);
        expect(graph.nodes.filter(node => node.metadata?.workflowSlot?.role === "input")).toHaveLength(3);
        expect(graph.nodes.filter(node => draft.definition.outputNodeIds.includes(node.metadata?.workflowSlot?.nodeId || ""))).toHaveLength(3);
        expect(graph.nodes.find(node => node.metadata?.workflowSlot?.nodeId === "first-motion")?.title).toBe("前段动作参考视频");
        expect(graph.nodes.find(node => node.metadata?.workflowSlot?.nodeId === "second-motion")?.title).toBe("后段动作参考视频");
        expect(graph.nodes.every(node => !node.metadata?.storageKey && !node.metadata?.taskId)).toBe(true);
    });

    test("drafts do not share mutable prompts and reference controls remain editable", () => {
        const a = createContinuousVideoWorkflow("complete", operations), b = createContinuousVideoWorkflow("complete", operations);
        a.definition.nodes.find(node => node.id === "second-video")!.config!.prompt = "new direction";
        const second = b.definition.nodes.find(node => node.id === "second-video")!;
        expect(second.config?.prompt).toContain("@[node:last-frame]");
        expect(workflowEditableFields({ ...second, config: {} }, operations[0]).map(field => field.key)).toContain("videoEditOperation");
    });

    test("unavailable native processing is reported before opening an unusable draft", () => {
        expect(() => createContinuousVideoWorkflow("complete", operations.slice(0, 1))).toThrow("处理能力不可用");
        expect(() => createContinuousVideoWorkflow("first", [operations[0], { ...operations[1], unavailableReason: "媒体编码器不可用" }])).toThrow("媒体编码器不可用");
    });
});
