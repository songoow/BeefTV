import type { CanvasWorkflow, WorkflowDefinition, WorkflowNode, WorkflowOperation } from "@/services/api/canvas-workflows";
import { canvasNodeMentionToken } from "./canvas-resource-references";

export type ContinuousVideoStage = "complete" | "first" | "continue";

export const continuousVideoStarters: Array<{ stage: ContinuousVideoStage; name: string; description: string }> = [
    { stage: "complete", name: "双段连续视频", description: "角色造型图与两段动作视频 → 前段 → 实际末帧 → 后段。连续执行，中途不暂停验收；默认 11＋10 秒，可在节点属性中修改。" },
    { stage: "first", name: "双段视频 · 前段与末帧", description: "先生成前段和实际末帧，人工验收后再用续接模板。默认前段 11 秒，可在节点属性中修改。" },
    { stage: "continue", name: "双段视频 · 从已验收前段续接", description: "输入冻结的角色造型图、已验收前段成片和后段动作视频，仅生成后段。默认后段 10 秒，可在节点属性中修改。" },
];

/** A native workflow draft: the editor, compiler and runner remain authoritative. */
export function createContinuousVideoWorkflow(stage: ContinuousVideoStage, operations: WorkflowOperation[]): Pick<CanvasWorkflow, "name" | "description" | "definition"> {
    const starter = continuousVideoStarters.find(item => item.stage === stage)!;
    const operation = (id: string) => {
        const found = operations.find(item => item.id === id);
        if (!found) throw new Error("连续视频模板所需的处理能力不可用，请更新应用后重试");
        if (found.unavailableReason) throw new Error(found.unavailableReason);
        return found;
    };
    const generation = operation("generate.video"), frame = operation("video.frame");
    const input = (id: string, title: string, mediaType: "image" | "video"): WorkflowNode => ({ id, title, mediaType, role: "input", useDefault: false });
    const mention = canvasNodeMentionToken;
    const characterPrompt = `以 ${mention("character")} 为最终角色造型：保持两人的脸部特征、头身比例、发型、服饰配色和画面风格。两位角色的对应关系保持一致，不交换身份。`;
    const generate = (id: string, title: string, seconds: number, prompt: string): WorkflowNode => ({
        id, title, mediaType: "video", role: "operation", operationId: generation.id, operationVersion: generation.version,
        config: { prompt, videoSeconds: seconds, videoEditOperation: "reference_to_video" },
    });
    const nodes: WorkflowNode[] = [input("character", "最终角色造型图（已冻结）", "image")];
    const edges: WorkflowDefinition["edges"] = [];
    const connect = (fromNodeId: string, toNodeId: string, toPort: string, order = 0) => edges.push({ fromNodeId, toNodeId, fromPort: "output", toPort, order });
    if (stage === "continue") {
        nodes.push(input("first-video", "已验收前段成片", "video"));
    } else {
        nodes.push(input("first-motion", "前段动作参考视频", "video"));
        nodes.push(generate("first-video", "生成前段", 11,
            `${characterPrompt}\n以 ${mention("first-motion")} 参考动作、节奏和运镜；人物外观以角色造型图为准。保持互动方向、接触关系和肢体结构自然。`));
        connect("character", "first-video", "referenceImages");
        connect("first-motion", "first-video", "referenceVideos");
    }
    nodes.push({ id: "last-frame", title: "前段实际末帧（连续性参考）", mediaType: "image", role: "operation", operationId: frame.id, operationVersion: frame.version, config: { mode: "last" } });
    connect("first-video", "last-frame", "video");
    if (stage !== "first") {
        nodes.push(input("second-motion", "后段动作参考视频", "video"));
        nodes.push(generate("second-video", "生成后段", 10,
            `${characterPrompt}\n以 ${mention("last-frame")} 参考前段结束时的人物位置、朝向、姿态、镜头和光线，自然延续互动；它只用于连续性参考。以 ${mention("second-motion")} 参考后续动作、节奏和运镜；保持角色造型图中的身份与比例，避免把动作参考中的人物外观带入结果。`));
        connect("character", "second-video", "referenceImages");
        connect("last-frame", "second-video", "referenceImages", 1);
        connect("second-motion", "second-video", "referenceVideos");
    }
    return { name: starter.name, description: starter.description, definition: {
        version: 1, editorLayoutVersion: 2, presentationVersion: 1, nodes, edges,
        outputNodeIds: stage === "first" ? ["first-video", "last-frame"] : stage === "continue" ? ["last-frame", "second-video"] : ["first-video", "last-frame", "second-video"],
    } };
}
