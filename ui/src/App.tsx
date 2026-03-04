import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const queryClient = new QueryClient();

function Board() {
  return (
    <div className="p-8">
      <p className="text-gray-500">Task board coming soon...</p>
    </div>
  );
}

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <div className="min-h-screen bg-gray-950 text-white">
        <header className="border-b border-gray-800 px-6 py-4">
          <h1 className="text-2xl font-bold">Drem Orchestrator</h1>
        </header>
        <main>
          <Board />
        </main>
      </div>
    </QueryClientProvider>
  );
}

export default App;
