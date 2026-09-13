// P0-6：把 zh-CN 字典的「键树」投影为宽字符串形状，供 en-US 用 satisfies 做编译期对齐：
// 键缺失 / 键多余 / 值不是字符串都会让 `tsc -b` 失败；值类型放宽为 string（不要求与中文逐字相同）。
export type DeepStringShape<T> = {
  [Key in keyof T]: T[Key] extends string ? string : DeepStringShape<T[Key]>;
};
