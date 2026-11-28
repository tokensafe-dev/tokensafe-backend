export async function GET() {
  return Response.json({ orders: [] });
}

export async function POST() {
  return Response.json({ id: 1 }, { status: 201 });
}
